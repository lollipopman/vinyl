# yamlfmt

Based on gofmt, yamlfmt formats yaml files into a canonical format
retaining comments and setting the indentation to 2 spaces, including
for sequences, e.g.:

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

## Usage

    $ ./yamlfmt --help
    usage: yamlfmt [flags] [path ...]
      -cpuprofile string
            write cpu profile to this file
      -d    display diffs instead of rewriting files
      -l    list files whose formatting differs from yamlfmt's
      -w    write result to (source) file instead of stdout

Without an explicit path, it processes from standard input. Given a
file, it operates on that file; given a directory, it operates on all
.yaml and .yml files in that directory, recursively.

By default, yamlfmt prints the reformatted sources to standard output.
