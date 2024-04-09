FROM cgr.dev/chainguard/go@sha256:bc4b9e98ca6c4304c93b537c0c8f40715d0b11de2600691700b562fa47f0571c as builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY main.go main.go
COPY cmd cmd
COPY pkg pkg


RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main

FROM cgr.dev/chainguard/wolfi-base@sha256:19f93882ea0865d92eb467e4d82eb19bc4f0bc7f153ab770ed8e45761c4febb6

RUN apk add openssh git
RUN adduser git -D && passwd -d git

COPY ./sh /bin

COPY --from=builder /app/main /bin/operator
ENTRYPOINT ["/bin/entrypoint.sh"]