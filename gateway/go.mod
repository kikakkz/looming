module github.com/kikakkz/looming/gateway

go 1.27.3

require (
	github.com/kikakkz/looming/platform/go v0.0.0
	github.com/stretchr/testify v1.12.1
	go.uber.org/goleak v1.3.0
)

require go.yaml.in/yaml/v3 v3.0.5 // indirect

replace github.com/kikakkz/looming/platform/go => ../platform/go
