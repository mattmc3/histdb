# histdb

Shell history in SQLite.

TODO: Placeholder

## Install

```sh
go install github.com/mattmc3/histdb@latest
```

Or build from a clone:

```sh
just build      # -> bin/histdb
just install    # -> $GOBIN
```

## Usage

```sh
histdb init <shell>   # print shell integration to eval
histdb -v, --version
histdb -h, --help
```

Enable in Zsh by adding this to `.zshrc`:

```zsh
source <(histdb init zsh)
```

## Development

```sh
just            # list recipes
just check      # fmt, vet, test
just run init zsh
```

## License

[MIT](LICENSE)
