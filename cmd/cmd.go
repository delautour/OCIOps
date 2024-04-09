package cmd

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/google/uuid"
	"io"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"log"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/registry/remote"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	d "k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"

	"oras.land/oras-go/v2/content/file"

	"github.com/go-git/go-billy/v5/memfs"

	git "github.com/go-git/go-git/v5"
)

var argoAppGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "applications",
}

func Run() {
	ctx := context.Background()
	kconf, err := getKubeConfig()

	dyn, err := d.NewForConfig(kconf)
	if err != nil {
		panic(err)
	}

	// Lookup the user
	u, err := user.Lookup("git")
	if err != nil {
		log.Fatalf("Failed to lookup user: %v", err)
	}

	// Convert the UID and GID to integer
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		log.Fatalf("Failed to convert UID to integer: %v", err)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		log.Fatalf("Failed to convert GID to integer: %v", err)
	}

	// Change the user context
	err = syscall.Setgid(gid)
	if err != nil {
		log.Fatalf("Failed to set GID: %v", err)
	}
	err = syscall.Setuid(uid)
	if err != nil {
		log.Fatalf("Failed to set UID: %v", err)
	}

	apps, err := dyn.Resource(argoAppGVR).Watch(ctx, metav1.ListOptions{})
	if err != nil {
		panic(err)
	}

	for item := range apps.ResultChan() {
		unstructuredObj, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(item.Object)
		repoURL, _, _ := unstructured.NestedString(unstructuredObj, "spec", "source", "repoURL")
		revision, _, _ := unstructured.NestedString(unstructuredObj, "spec", "source", "targetRevision")
		artifactName := strings.TrimPrefix(repoURL, "ssh://git@git.default.svc.cluster.local:")

		watchForUpdates(ctx, artifactName, revision)
	}
}

var watching = make(map[string]map[string]struct{})

func watchForUpdates(ctx context.Context, name string, revision string) {
	fmt.Println("Watching for updates", name, revision)

	if _, ok := watching[name]; !ok {
		watching[name] = make(map[string]struct{})
	}
	if _, ok := watching[name][revision]; ok {
		fmt.Println("Already watching", name, revision)
		return
	}
	watching[name][revision] = struct{}{}

	ticker := time.NewTicker(5 * time.Second)
	go func() {
		err := updateGitRepoWithOciImage(ctx, name, revision)
		if err != nil {
			fmt.Println(err)
		}
		for range ticker.C {
			err := updateGitRepoWithOciImage(ctx, name, revision)
			if err != nil {
				fmt.Println(err)
			}
		}
	}()
}

func updateGitRepoWithOciImage(ctx context.Context, artifactName, tag string) error {
	fmt.Printf("Downloading %s:%s\n", artifactName, tag)

	gitRepo, err := getGitRepo(artifactName)
	if err != nil {
		return err
	}

	wt, err := gitRepo.Worktree()
	if err != nil {
		return err
	}

	branch := plumbing.NewBranchReferenceName(tag)

	_, err = gitRepo.Reference(branch, false)
	if err == nil {
		err = wt.Checkout(&git.CheckoutOptions{
			Branch: branch,
			Force:  true,
		})
	} else if errors.Is(err, plumbing.ErrReferenceNotFound) {
		err = wt.Checkout(&git.CheckoutOptions{
			Branch: branch,
			Create: true,
		})
	}

	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			ref := plumbing.NewHashReference(branch, plumbing.ZeroHash)
			err = gitRepo.Storer.SetReference(ref)
			if err != nil {
				fmt.Println("Error creating branch", err)
				return err
			}
		} else {

			fmt.Println("Error checking out branch", err)
			return err
		}
	}

	id := uuid.New()
	store, err := file.New("/tmp/" + id.String())
	if err != nil {
		return err
	}
	defer store.Close()
	defer os.RemoveAll("/tmp/" + id.String())

	reg := "zot.default.svc.cluster.local:5000"
	//reg = "localhost:5000"
	repo, err := remote.NewRepository(reg + "/" + artifactName)
	repo.PlainHTTP = true
	if err != nil {
		return err
	}

	manifestDescriptor, err := oras.Copy(ctx, repo, tag, store, tag, oras.DefaultCopyOptions)
	if err != nil {
		return err
	}

	wt.Filesystem.Remove("/")
	err = copyBillyFilesystem(osfs.New("/tmp/"+id.String()), wt.Filesystem)
	if err != nil {
		return err
	}
	err = wt.AddWithOptions(&git.AddOptions{All: true})
	if err != nil {
		return err
	}

	// check if there are any changes
	status, err := wt.Status()
	if err != nil {
		return err
	}
	if status.IsClean() {
		return nil
	}

	hash, err := wt.Commit(manifestDescriptor.Digest.String(), &git.CommitOptions{
		Author: &object.Signature{
			Name:  "oci",
			Email: "oci@chase.io",
			When:  time.Now(),
		},
	})
	gitRepo.Storer.SetReference(plumbing.NewHashReference(branch, hash))
	return err
}

func getGitRepo(name string) (*git.Repository, error) {
	path := "/home/git/" + name

	wt := memfs.New()
	fs := osfs.New(path)
	s := filesystem.NewStorage(fs, cache.NewObjectLRUDefault())

	fmt.Println("Init repo")

	_, err := git.InitWithOptions(s, wt, git.InitOptions{
		DefaultBranch: "refs/heads/__do_not_use",
	})
	if err != nil && !errors.Is(err, git.ErrRepositoryAlreadyExists) {
		fmt.Println("Error creating repo", err)
		return nil, err
	}
	if err == nil {
		fmt.Println("Repo created")
	}

	repo, err := git.Open(s, wt)
	fmt.Println("Opened repo")
	if err != nil {
		fmt.Println("Error opening repo", err)
	}

	return repo, err
}

func getKubeConfig() (*rest.Config, error) {
	kconf, err := rest.InClusterConfig()
	if err == nil {
		return kconf, nil
	}

	usr, _ := user.Current()
	dir := usr.HomeDir
	kconf, err = clientcmd.BuildConfigFromFlags("", filepath.Join(dir, ".kube", "config"))
	if err != nil {
		panic(err)
	}
	return kconf, err
}

func copyBillyFilesystem(srcFs, dstFs billy.Filesystem) error {
	files, err := srcFs.ReadDir("/")
	if err != nil {
		return err
	}

	for _, file := range files {
		srcPath := file.Name()
		dstPath := srcPath

		if file.IsDir() {
			err := dstFs.MkdirAll(dstPath, file.Mode())
			if err != nil {
				return err
			}
		} else {
			srcFile, err := srcFs.Open(srcPath)
			if err != nil {
				return err
			}
			defer srcFile.Close()

			dstFile, err := dstFs.Create(dstPath)
			if err != nil {
				return err
			}
			defer dstFile.Close()

			_, err = io.Copy(dstFile, srcFile)
			if err != nil {
				return err
			}
		}
	}

	return nil
}
