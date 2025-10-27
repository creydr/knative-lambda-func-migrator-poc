# Knative Lambda Migrator

A CLI tool that automatically migrates AWS Lambda handler functions to Knative-compatible function handlers. This tool analyzes your Lambda function code and transforms it into a format that works with Knative Functions, making it easier to move from AWS Lambda to Kubernetes-native serverless platforms.

## Usage

```bash
go run github.com/creydr/knative-lambda-func-migrator-poc/cmd@latest -input <path-to-lambda-file> [-output <path-to-output-file>]
```

### Options

- `-input`: Path to the Go file containing your AWS Lambda handler (required)
- `-output`: Path to write the transformed code (optional, defaults to stdout)

## Examples

### Example 1: Simple Handler in the same File

**Input** (`handler.go`):
```go
package main

import (
    "context"
    "encoding/json"
    "github.com/aws/aws-lambda-go/lambda"
)

type Request struct {
    Name string `json:"name"`
}

type Response struct {
    Message string `json:"message"`
}

func handleRequest(ctx context.Context, event json.RawMessage) (json.RawMessage, error) {
    var req Request
    if err := json.Unmarshal(event, &req); err != nil {
        return nil, err
    }

    response := Response{
        Message: "Hello, " + req.Name,
    }

    return json.Marshal(response)
}

func main() {
    lambda.Start(handleRequest)
}
```

**Command**:
```bash
lambda-migrator -input handler.go -output knative-handler.go
```

**Output** (`knative-handler.go`):
```go
package main

import (
    "context"
    "encoding/json"
    "net/http"
    "io"
    "log"
)

type Request struct {
    Name string `json:"name"`
}

type Response struct {
    Message string `json:"message"`
}

func handleRequest(ctx context.Context, event json.RawMessage) (json.RawMessage, error) {
    var req Request
    if err := json.Unmarshal(event, &req); err != nil {
        return nil, err
    }

    response := Response{
        Message: "Hello, " + req.Name,
    }

    return json.Marshal(response)
}

type Handler struct {
}

func New() *Handler {
    return &Handler{}
}

func (h *Handler) Handle(ctx context.Context, w http.ResponseWriter, r *http.Request) {
    body, _ := io.ReadAll(r.Body)
    result, err := handleRequest(ctx, body)
    if err != nil {
        log.Printf("Handler error: %v", err)
        w.WriteHeader(500)
        return
    }
    json.NewEncoder(w).Encode(result)
}
```

### Example 2: Handler in Separate Package

**Input** (`cmd/main.go`):
```go
package main

import (
    "github.com/aws/aws-lambda-go/lambda"
    "github.com/myorg/myapp/pkg/handler"
)

func main() {
    lambda.Start(handler.HandleRequest)
}
```

**Handler** (`pkg/handler/handler.go`):
```go
package handler

import (
    "context"
    "encoding/json"
)

type Order struct {
    OrderID string  `json:"order_id"`
    Amount  float64 `json:"amount"`
}

func HandleRequest(ctx context.Context, event json.RawMessage) (json.RawMessage, error) {
    var order Order
    if err := json.Unmarshal(event, &order); err != nil {
        return nil, err
    }

    // Process order...

    return json.Marshal(map[string]string{
        "status": "processed",
        "order_id": order.OrderID,
    })
}
```

**Command**:
```bash
lambda-migrator -input cmd/main.go -output cmd/knative-main.go
```

**Output** (`cmd/knative-main.go`):
```go
package main

import (
    "github.com/myorg/myapp/pkg/handler"
    "context"
    "net/http"
    "io"
    "encoding/json"
    "log"
)

type Handler struct {
}

func New() *Handler {
    return &Handler{}
}

func (h *Handler) Handle(ctx context.Context, w http.ResponseWriter, r *http.Request) {
    body, _ := io.ReadAll(r.Body)
    result, err := handler.HandleRequest(ctx, body)
    if err != nil {
        log.Printf("Handler error: %v", err)
        w.WriteHeader(500)
        return
    }
    json.NewEncoder(w).Encode(result)
}
```

### Example 3: Error-Only Handler (No Response Body)

**Input**:
```go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "github.com/aws/aws-lambda-go/lambda"
)

func handleRequest(ctx context.Context, event json.RawMessage) error {
    var data map[string]string
    if err := json.Unmarshal(event, &data); err != nil {
        return err
    }

    name := data["name"]
    if name == "" {
        return fmt.Errorf("name cannot be empty")
    }

    fmt.Printf("Hello, %s\n", name)
    return nil
}

func main() {
    lambda.Start(handleRequest)
}
```

**Output** (to stdout):
```bash
lambda-migrator -input handler.go
```

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "io"
    "log"
)

func handleRequest(ctx context.Context, event json.RawMessage) error {
    var data map[string]string
    if err := json.Unmarshal(event, &data); err != nil {
        return err
    }

    name := data["name"]
    if name == "" {
        return fmt.Errorf("name cannot be empty")
    }

    fmt.Printf("Hello, %s\n", name)
    return nil
}

type Handler struct {
}

func New() *Handler {
    return &Handler{}
}

func (h *Handler) Handle(ctx context.Context, w http.ResponseWriter, r *http.Request) {
    body, _ := io.ReadAll(r.Body)
    err := handleRequest(ctx, body)
    if err != nil {
        log.Printf("Handler error: %v", err)
        w.WriteHeader(500)
        return
    }
}
```

## Supported Lambda Handler Signatures

The tool supports all 9 valid [AWS Lambda handler signatures](https://docs.aws.amazon.com/lambda/latest/dg/golang-handler.html#golang-handler-signatures):

1. `func ()`
2. `func () error`
3. `func () (TOut, error)`
4. `func (TIn) error`
5. `func (TIn) (TOut, error)`
6. `func (context.Context) error`
7. `func (context.Context) (TOut, error)`
8. `func (context.Context, TIn) error`
9. `func (context.Context, TIn) (TOut, error)`

Where:
- `TIn` is any type that can be unmarshalled from JSON (passed as `[]byte`)
- `TOut` is any type that can be marshaled to JSON