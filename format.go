package yamlfmt

import (
	"bytes"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

const indent = 2

// Format reads in a yaml document and outputs the yaml in a standard format.
// Indents are set to 2
// Lists are not indented
func Format(filename string, r io.Reader) ([]byte, error) {
	dec := yaml.NewDecoder(r)
	out := bytes.NewBuffer(nil)
	numDocs := 0
	for {
		enc := yaml.NewEncoder(out)
		enc.SetIndent(indent)
		defer enc.Close()
		var doc yaml.Node
		err := dec.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed decoding: %s", err)
		}
		// Only output the yaml doc separator if there is more than one document,
		// mirrors gopkg.in/yaml.v3
		if numDocs > 0 {
			out.WriteString("---\n")
		}
		err = enc.Encode(&doc)
		if err != nil {
			return nil, fmt.Errorf("failed encoding: %s", err)
		}
		enc.Close()
		numDocs++
	}
	// HACK: At present gopkg.in/yaml.v3 does not perform duplicate key checking
	// when decoding into a yaml.Node[1]. So we decode into an interface{} and
	// throw away the result, as we are only interested in the errors, if any.
	//
	// [1]: https://github.com/go-yaml/yaml/issues/814
	var unused interface{}
	err := yaml.Unmarshal(out.Bytes(), &unused)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filename, err)
	}
	return out.Bytes(), nil
}
