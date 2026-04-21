package schema

import _ "embed"

//go:embed embedded/gatr.schema.json
var SchemaJSON []byte

// SchemaURI is the canonical $id used when registering the embedded schema with
// jsonschema.Compiler.
const SchemaURI = "https://gatr.dev/schema/v4/gatr.schema.json"
