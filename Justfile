
set shell := ["cmd.exe", "/c"]

default: rollout

oras-alice:
    cd examples/alice && oras push --plain-http localhost:5000/alice:latest .

oras-bortrand:
    cd examples/bortrand && oras push --plain-http localhost:5000/bortrand:stable .

oras: oras-alice oras-bortrand

deploy:
    kubectl apply -f manifests/

build:
    nerdctl --namespace k8s.io build -t ociops:latest .

rollout: build
    kubectl rollout restart deployment/git
