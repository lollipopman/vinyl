# VINYL Inscribes Nettlesome YAML Legibly

VINYL formats yaml files into a canonical format retaining comments and
setting the indentation by default to two spaces, including for
sequences, e.g.:

``` yaml
# List of recipes
recipies:
  # todo
  - pizza: {}
  # todo
  - lasagna: {}
  - barley soup:
      ingredients:
        - barley
        - onions
        - beef
      serves: 4
```

It uses code from
[gofmt](https://github.com/golang/go/blob/master/src/cmd/gofmt/gofmt.go)
and [go-yaml](https://gopkg.in/yaml.v3) does the heavy lifting of
parsing the yaml and formatting the output

## Usage

    $ ./vinyl --help
    usage: vinyl [flags] [path ...]
      -cpuprofile string
            write cpu profile to this file
      -d    display diffs instead of rewriting files
      -i uint
            number of spaces to indent (default 2)
      -l    list files whose formatting differs from vinyl's
      -w    write result to (source) file instead of stdout

Without an explicit path, it processes from standard input. Given a
file, it operates on that file; given a directory, it operates on all
.yaml and .yml files in that directory, recursively.

By default, vinyl prints the reformatted sources to standard output.

## Credits

-   [yamlfmt](https://github.com/stuart-warren/yamlfmt): VINYL is a fork
    of `yamlfmt`
-   [gofmt](https://github.com/golang/go/blob/master/src/cmd/gofmt/gofmt.go):
    Code to format all files and handle errors
-   [go-yaml](https://gopkg.in/yaml.v3) YAML parser
