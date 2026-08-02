bin := "histdb"
outdir := "bin"

# list recipes
default:
    @just --list

# build the binary into bin/
build:
    go build -o {{outdir}}/{{bin}} .

# run tests
test:
    go test ./...

# run tests with verbosity
testv:
    go test -v ./...

# run tests with coverage summary
cover:
    go test -cover ./...

# format sources
fmt:
    go fmt ./...

# report suspect constructs
vet:
    go vet ./...

# tidy go.mod and go.sum
tidy:
    go mod tidy

# fmt, vet, test
check: fmt vet test

# run histdb with args, eg: just run init zsh
run *args:
    go run . {{args}}

# install to GOBIN
install:
    go install .

# remove build output
clean:
    rm -rf {{outdir}}
