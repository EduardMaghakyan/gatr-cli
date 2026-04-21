module github.com/EduardMaghakyan/gatr-cli/pkg/stripe

go 1.24

require (
	github.com/BurntSushi/toml v1.4.0
	github.com/EduardMaghakyan/gatr-cli/pkg/schema v0.0.0-00010101000000-000000000000
	github.com/stretchr/testify v1.11.1
	github.com/stripe/stripe-go/v82 v82.5.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.1 // indirect
	go.yaml.in/yaml/v2 v2.4.2 // indirect
	golang.org/x/text v0.18.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	sigs.k8s.io/yaml v1.5.0 // indirect
)

replace github.com/EduardMaghakyan/gatr-cli/pkg/schema => ../schema
