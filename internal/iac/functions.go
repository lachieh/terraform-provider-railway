// Ported from railwayapp/cli src/iac/functions.rs (MIT, Copyright (c) Railway Corp.)
//
// Railway Functions: single-file Bun services whose source is base64-encoded
// into the deploy start command (`./run.sh <base64>`). The platform models
// them as image services on the function runtime image; the SDKs expose them
// as `fn(name, { code })`.

package iac

import (
	"encoding/base64"
	"strings"
	"unicode/utf8"
)

// FunctionImagePrefix prefixes every function runtime image.
const FunctionImagePrefix = "ghcr.io/railwayapp/function"

const startCommandPrefix = "./run.sh "

func IsFunctionImage(image string) bool {
	return strings.HasPrefix(image, FunctionImagePrefix)
}

// IsFunctionResource is true for a graph node that is a function: declared
// with `fn()` (kind `function`), or a service running the function runtime
// image.
func IsFunctionResource(resource any) bool {
	if kind, ok := FieldStrOK(resource, "type"); !ok || kind != "service" {
		return false
	}
	if kind, ok := FieldStrOK(resource, "kind"); ok && kind == "function" {
		return true
	}
	image, ok := FieldStrOK(Field(resource, "source"), "image")
	return ok && IsFunctionImage(image)
}

// DecodeFunctionCode decodes the source code carried by a function start
// command. ok is false when the command is not in the `./run.sh <base64>` form
// or the payload is not UTF-8, in which case callers fall back to the raw
// string.
func DecodeFunctionCode(startCommand string) (string, bool) {
	encoded, found := strings.CutPrefix(startCommand, startCommandPrefix)
	if !found {
		return "", false
	}
	encoded = strings.TrimSpace(encoded)
	if encoded == "" || strings.Contains(encoded, " ") || strings.ContainsAny(encoded, "\r\n") {
		return "", false
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || !utf8.Valid(decoded) {
		return "", false
	}
	return string(decoded), true
}

// EncodeFunctionCode encodes source code the way `railway functions push` and
// the SDKs do.
func EncodeFunctionCode(code string) string {
	return startCommandPrefix + base64.StdEncoding.EncodeToString([]byte(code))
}

// FunctionCodeOf is the decoded source of a service's `deploy.startCommand`,
// when it is a function start command.
func FunctionCodeOf(deploy any) (string, bool) {
	command, ok := FieldStrOK(deploy, "startCommand")
	if !ok {
		return "", false
	}
	return DecodeFunctionCode(command)
}

// SourceCodeDiffLines returns unified diff lines (no file header) between two
// sources, plus the insertion and deletion counts. Lines are empty when the
// sources are identical.
func SourceCodeDiffLines(before, after string) (lines []string, insertions, deletions int) {
	oldLines := tokenizeLines(before)
	newLines := tokenizeLines(after)
	for index, group := range groupDiffOps(diffLines(oldLines, newLines), 3) {
		if index > 0 {
			lines = append(lines, "...")
		}
		for _, op := range group {
			emit := func(marker, text string) {
				lines = append(lines, marker+" "+strings.TrimRight(text, "\n\r"))
			}
			if op.equal {
				for offset := 0; offset < op.oldLen; offset++ {
					emit(" ", oldLines[op.oldIndex+offset])
				}
				continue
			}
			for offset := 0; offset < op.oldLen; offset++ {
				deletions++
				emit("-", oldLines[op.oldIndex+offset])
			}
			for offset := 0; offset < op.newLen; offset++ {
				insertions++
				emit("+", newLines[op.newIndex+offset])
			}
		}
	}
	return lines, insertions, deletions
}

// rustLineCount is Rust's str::lines().count(): a trailing newline does not
// start another line.
func rustLineCount(text string) int {
	count := strings.Count(text, "\n")
	if text != "" && !strings.HasSuffix(text, "\n") {
		count++
	}
	return count
}
