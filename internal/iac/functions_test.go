// Ported from railwayapp/cli src/iac/functions.rs (MIT, Copyright (c) Railway Corp.)

package iac

import "testing"

func TestRoundTripsSourceThroughTheStartCommand(t *testing.T) {
	code := "export default { fetch() { return new Response(\"hi\"); } }\n"
	decoded, ok := DecodeFunctionCode(EncodeFunctionCode(code))
	assertTrue(t, ok, "round trip failed")
	assertEqual(t, decoded, code)
}

func TestRejectsCommandsThatAreNotFunctionStartCommands(t *testing.T) {
	for _, command := range []string{
		"npm start",
		"./run.sh",
		"./run.sh not base64!",
		"./run.sh aGk= extra",
	} {
		t.Run(command, func(t *testing.T) {
			_, ok := DecodeFunctionCode(command)
			assertTrue(t, !ok, "%q decoded", command)
		})
	}
}

func TestClassifiesFunctionResourcesByKindOrImage(t *testing.T) {
	assertTrue(t, IsFunctionResource(obj(`{ "type": "service", "kind": "function" }`)), "kind function")
	assertTrue(t, IsFunctionResource(obj(`{
		"type": "service",
		"kind": "docker-image",
		"source": { "type": "image", "image": "ghcr.io/railwayapp/function:1.2.3" }
	}`)), "function image")
	assertTrue(t, !IsFunctionResource(obj(`{
		"type": "service",
		"kind": "docker-image",
		"source": { "type": "image", "image": "ghcr.io/acme/api:1" }
	}`)), "other image")
	assertTrue(t, !IsFunctionResource(obj(`{ "type": "database", "kind": "function" }`)), "database")
}

func TestDiffLinesCarryMarkersAndCounts(t *testing.T) {
	lines, insertions, deletions := SourceCodeDiffLines("a\nb\nc\n", "a\nB\nc\n")
	assertJSON(t, lines, []string{"  a", "- b", "+ B", "  c"})
	assertEqual(t, insertions, 1)
	assertEqual(t, deletions, 1)
	same, _, _ := SourceCodeDiffLines("same\n", "same\n")
	assertEqual(t, len(same), 0)
}

// TestSourceCodeDiffLinesMatchesSimilar pins outputs recorded from the
// `similar` crate on edits whose hunks can be placed more than one way.
func TestSourceCodeDiffLinesMatchesSimilar(t *testing.T) {
	for _, fixture := range []struct {
		before, after string
		want          []string
	}{
		{
			before: "b\na\na\ny\r}\na\na\nzx\r\na\n\nc\nzb\n\na\n\n}\na\nz",
			after:  "b\nb\na\na\ny\r}\na\na\nzx\r\n\nc\nzb\n\na\na\n\n}\na\nz",
			want:   []string{"  b", "+ b", "  a", "  a", "  y", "...", "  a", "  a", "  zx", "- a", "  ", "  c", "  zb", "  ", "  a", "+ a", "  ", "  }", "  a"},
		},
		{
			before: "a\n\nzc\na\nc\nb\ny\rx\r\na\na\nb\ny\rx\r\na\nza\na\nx\r\nx\r\nx\r\nc\nc\n",
			after:  "a\n\nc\nzc\na\na\nc\nb\ny\rx\r\na\na\nb\ny\rx\r\na\nza\na\nx\r\nx\r\nx\r\nc\nc\n",
			want:   []string{"  a", "  ", "+ c", "  zc", "  a", "+ a", "  c", "  b", "  y"},
		},
		{
			before: "x\r\n\n}\n}\nzy\r}\nzy\ra\ny\rx\r\nb\n",
			after:  "x\r\n\nx\r\n}\n}\nzy\r}\nc\n}\nzy\ra\ny\rx\r\nb\n",
			want:   []string{"  x", "  ", "+ x", "  }", "  }", "  zy", "  }", "+ c", "+ }", "  zy", "  a", "  y"},
		},
	} {
		lines, insertions, deletions := SourceCodeDiffLines(fixture.before, fixture.after)
		assertJSON(t, lines, fixture.want)
		wantInsertions, wantDeletions := 0, 0
		for _, line := range fixture.want {
			switch line[0] {
			case '+':
				wantInsertions++
			case '-':
				wantDeletions++
			}
		}
		assertEqual(t, insertions, wantInsertions)
		assertEqual(t, deletions, wantDeletions)
	}
}
