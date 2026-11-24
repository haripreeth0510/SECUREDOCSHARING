# Go Backend Services — Complete Guide

## Why Go for Backend?

- Compiled, statically typed, fast startup & low memory footprint
- Built-in concurrency with goroutines & channels
- Strong standard library — HTTP, JSON, crypto, testing all built-in
- Single binary deployment — no runtime dependencies

---\_

## Core Language Concepts

### Types & Structs

```go
type User struct {
    ID    int    `json:"id"`
    Name  string `json:"name"`
    Email string `json:"email"`
}
```

### Error Handling

Go returns errors as values — no exceptions.

```go
func getUser(id int) (*User, error) {
    if id <= 0 {
        return nil, fmt.Errorf("invalid id: %d", id)
    }
    return &User{ID: id}, nil
}
```

### Interfaces

Implemented implicitly — no `implements` keyword needed.

```go
type UserRepository interface {
    FindByID(id int) (*User, error)
    Save(user *User) error
}
```

### Goroutines & Channels

Go's concurrency primitives.

```go
// Goroutine — runs concurrently
go func() {
    // do work
}()

// Buffered channel
ch := make(chan int, 1)
ch <- 42
val := <-ch
```

---

## HTTP Server

Go's `net/http` is production-ready without a framework (Go 1.22+):

```go
func main() {
    mux := http.NewServeMux()
    mux.HandleFunc("GET /users/{id}", getUserHandler)
    mux.HandleFunc("POST /users", createUserHandler)

    log.Println("Listening on :8080")
    http.ListenAndServe(":8080", mux)
}

func getUserHandler(w http.ResponseWriter, r *http.Request) {
    id := r.PathValue("id") // Go 1.22+
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]string{"id": id})
}
```

---

## Project Structure

```
myapp/
├── cmd/
│   └── api/
│       └── main.go          # entry point
├── internal/
│   ├── handler/             # HTTP handlers
│   ├── service/             # business logic
│   ├── repository/          # data access layer
│   └── model/               # domain types / structs
├── pkg/                     # shared/exported packages
├── go.mod
└── go.sum
```

- `internal/` — code that **cannot** be imported by external packages
- `cmd/` — entry points (supports multiple binaries)
- `pkg/` — reusable code that can be imported externally

---

## Key Packages & Libraries

| Concern     | Standard Library      | Popular Third-Party              |
| ----------- | --------------------- | -------------------------------- |
| HTTP Router | `net/http` (Go 1.22+) | `chi`, `gin`, `echo`             |
| JSON        | `encoding/json`       | `go-json` (faster drop-in)       |
| Database    | `database/sql`        | `pgx` (Postgres), `sqlx`, `GORM` |
| Config      | `os.Getenv`           | `viper`                          |
| Logging     | `log/slog` (Go 1.21+) | `zap`, `zerolog`                 |
| Validation  | —                     | `go-playground/validator`        |
| Testing     | `testing` (built-in)  | `testify`                        |
| Migrations  | —                     | `golang-migrate`                 |

---

## Database Pattern

Using `database/sql` with the repository pattern:

```go
// internal/repository/user.go

type userRepo struct {
    db *sql.DB
}

func NewUserRepo(db *sql.DB) UserRepository {
    return &userRepo{db: db}
}

func (r *userRepo) FindByID(ctx context.Context, id int) (*model.User, error) {
    var u model.User
    err := r.db.QueryRowContext(ctx,
        "SELECT id, name, email FROM users WHERE id = $1", id,
    ).Scan(&u.ID, &u.Name, &u.Email)
    if err != nil {
        return nil, fmt.Errorf("FindByID: %w", err)
    }
    return &u, nil
}
```

---

## Middleware Pattern

Middleware wraps an `http.Handler` — chain as many as you need:

```go
func authMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        token := r.Header.Get("Authorization")
        if token == "" {
            http.Error(w, "unauthorized", http.StatusUnauthorized)
            return
        }
        next.ServeHTTP(w, r)
    })
}

func loggingMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        log.Printf("%s %s", r.Method, r.URL.Path)
        next.ServeHTTP(w, r)
    })
}

// Usage
mux.Handle("/api/", authMiddleware(loggingMiddleware(apiHandler)))
```

---

## Context — Critical for Backend

Always pass `context.Context` as the **first argument** for cancellation, timeouts, and request-scoped values:

```go
// Timeout example
ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
defer cancel()

rows, err := db.QueryContext(ctx, "SELECT * FROM users")

// Passing values through context
ctx = context.WithValue(ctx, "userID", 42)
userID := ctx.Value("userID").(int)
```

---

## Dependency Injection

No DI framework needed — pass dependencies via constructors:

```go
// Wire everything in main.go
func main() {
    db, _ := sql.Open("pgx", os.Getenv("DATABASE_URL"))

    userRepo    := repository.NewUserRepo(db)
    userService := service.NewUserService(userRepo)
    userHandler := handler.NewUserHandler(userService)

    mux := http.NewServeMux()
    userHandler.RegisterRoutes(mux)

    http.ListenAndServe(":8080", mux)
}
```

---

## Starter Project Scaffold

### `go.mod`

```
module github.com/yourname/myapp

go 1.22
```

### `cmd/api/main.go`

```go
package main

import (
    "log"
    "net/http"
)

func main() {
    mux := http.NewServeMux()
    // register routes here

    log.Println("Server running on :8080")
    log.Fatal(http.ListenAndServe(":8080", mux))
}
```

### `internal/model/user.go`

```go
package model

type User struct {
    ID    int    `json:"id"`
    Name  string `json:"name"`
    Email string `json:"email"`
}
```

### `internal/handler/user.go`

```go
package handler

import (
    "encoding/json"
    "net/http"
)

type UserHandler struct{}

func (h *UserHandler) RegisterRoutes(mux *http.ServeMux) {
    mux.HandleFunc("GET /users/{id}", h.GetUser)
}

func (h *UserHandler) GetUser(w http.ResponseWriter, r *http.Request) {
    id := r.PathValue("id")
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]string{"id": id})
}
```

---

## Essential CLI Commands

```bash
# Initialize a new module
go mod init github.com/yourname/myapp

# Add a dependency
go get github.com/jackc/pgx/v5

# Run the app
go run ./cmd/api

# Build a binary
go build -o bin/api ./cmd/api

# Run all tests
go test ./...

# Format code
go fmt ./...

# Vet for common issues
go vet ./...

# Tidy up go.mod & go.sum
go mod tidy
```

---

## Key Things to Remember

| Concept         | Notes                                                            |
| --------------- | ---------------------------------------------------------------- |
| No classes      | Use structs + methods + interfaces                               |
| No exceptions   | Errors are return values — always check them                     |
| No DI framework | Constructor injection is idiomatic                               |
| `defer`         | Runs at end of function — use for cleanup (`defer rows.Close()`) |
| Pointers        | Used for mutability & avoiding copies — GC handles memory        |
| Go version      | Use **1.22+** for improved HTTP router with method + path params |
| `internal/`     | Enforced by the compiler — external packages cannot import it    |

---

## Recommended Learning Path

1. [Go Tour](https://go.dev/tour) — official interactive intro
2. [Effective Go](https://go.dev/doc/effective_go) — idiomatic patterns
3. [Standard Library Docs](https://pkg.go.dev/std) — your best friend
4. [Let's Go (Alex Edwards)](https://lets-go.alexedwards.net/) — best book for Go web apps
