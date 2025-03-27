# testutils [![Build Status](https://github.com/go-pkgz/testutils/workflows/build/badge.svg)](https://github.com/go-pkgz/testutils/actions) [![Go Report Card](https://goreportcard.com/badge/github.com/go-pkgz/testutils)](https://goreportcard.com/report/github.com/go-pkgz/testutils) [![Coverage Status](https://coveralls.io/repos/github/go-pkgz/testutils/badge.svg?branch=master)](https://coveralls.io/github/go-pkgz/testutils?branch=master)

Package `testutils` provides useful test helpers.

## Details

### Capture Utilities

- `CaptureStdout`: Captures stdout output from the provided function
- `CaptureStderr`: Captures stderr output from the provided function
- `CaptureStdoutAndStderr`: Captures both stdout and stderr from the provided function

These capture utilities are useful for testing functions that write directly to stdout/stderr. They redirect the standard outputs to a buffer and return the captured content as a string.

**Important Note**: The capture functions are not thread-safe if used in parallel tests. For concurrent tests, it's better to pass a custom io.Writer to the function under test instead.

### File Utilities

- `WriteTestFile`: Creates a temporary file with specified content and returns its path. The file is automatically cleaned up after the test completes.

### HTTP Test Utilities

- `MockHTTPServer`: Creates a test HTTP server with the given handler. Returns the server URL and a cleanup function.
- `HTTPRequestCaptor`: Returns a request captor and an HTTP handler that captures and records HTTP requests for later inspection.

### Test Containers

The `containers` package provides several test containers for integration testing:

- `SSHTestContainer`: SSH server container for testing SSH connections and operations
- `PostgresTestContainer`: PostgreSQL database container with automatic database creation
- `MySQLTestContainer`: MySQL database container with automatic database creation
- `MongoTestContainer`: MongoDB container with support for multiple versions (5, 6, 7)
- `LocalstackTestContainer`: LocalStack container with S3 service for AWS testing

## Install and update

`go get -u github.com/go-pkgz/testutils`

## Example Usage

### Capture Functions

```go
// Capture stdout
func TestMyFunction(t *testing.T) {
    output := testutils.CaptureStdout(t, func() {
        fmt.Println("Hello, World!")
    })
    
    assert.Equal(t, "Hello, World!\n", output)
}

// Capture stderr
func TestErrorOutput(t *testing.T) {
    errOutput := testutils.CaptureStderr(t, func() {
        fmt.Fprintln(os.Stderr, "Error message")
    })
    
    assert.Equal(t, "Error message\n", errOutput)
}

// Capture both stdout and stderr
func TestBothOutputs(t *testing.T) {
    stdout, stderr := testutils.CaptureStdoutAndStderr(t, func() {
        fmt.Println("Standard output")
        fmt.Fprintln(os.Stderr, "Error output")
    })
    
    assert.Equal(t, "Standard output\n", stdout)
    assert.Equal(t, "Error output\n", stderr)
}
```

### File Utilities

```go
// Create a temporary test file
func TestWithTempFile(t *testing.T) {
    content := "test file content"
    filePath := testutils.WriteTestFile(t, content)
    
    // Use the file in your test
    data, err := os.ReadFile(filePath)
    require.NoError(t, err)
    assert.Equal(t, content, string(data))
    
    // No need to clean up - it happens automatically when the test ends
}
```

### HTTP Test Utilities

```go
// Create a mock HTTP server
func TestWithMockServer(t *testing.T) {
    handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        w.Write([]byte("response"))
    })
    
    serverURL, _ := testutils.MockHTTPServer(t, handler)
    
    // Make requests to the server
    resp, err := http.Get(serverURL + "/path")
    require.NoError(t, err)
    defer resp.Body.Close()
    
    assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// Capture and inspect HTTP requests
func TestWithRequestCaptor(t *testing.T) {
    // Create a request captor
    captor, handler := testutils.HTTPRequestCaptor(t, nil)
    
    // Create a server with the capturing handler
    serverURL, _ := testutils.MockHTTPServer(t, handler)
    
    // Make a request
    http.Post(serverURL+"/api", "application/json", 
              strings.NewReader(`{"key":"value"}`))
    
    // Inspect the captured request
    req, _ := captor.GetRequest(0)
    assert.Equal(t, http.MethodPost, req.Method)
    assert.Equal(t, "/api", req.Path)
    assert.Equal(t, `{"key":"value"}`, string(req.Body))
}
```

### Test Containers

```go
// PostgreSQL test container
func TestWithPostgres(t *testing.T) {
    ctx := context.Background()
    pg := containers.NewPostgresTestContainer(ctx, t)
    defer pg.Close(ctx)
    
    db, err := sql.Open("postgres", pg.ConnectionString())
    require.NoError(t, err)
    defer db.Close()
    
    // run your tests with the database
}

// MySQL test container
func TestWithMySQL(t *testing.T) {
    ctx := context.Background()
    mysql := containers.NewMySQLTestContainer(ctx, t)
    defer mysql.Close(ctx)
    
    db, err := sql.Open("mysql", mysql.DSN())
    require.NoError(t, err)
    defer db.Close()
    
    // run your tests with the database
}

// MongoDB test container
func TestWithMongo(t *testing.T) {
    ctx := context.Background()
    mongo := containers.NewMongoTestContainer(ctx, t, 7) // version 7
    defer mongo.Close(ctx)
    
    coll := mongo.Collection("test_db")
    _, err := coll.InsertOne(ctx, bson.M{"test": "value"})
    require.NoError(t, err)
}

// SSH test container
func TestWithSSH(t *testing.T) {
    ctx := context.Background()
    ssh := containers.NewSSHTestContainer(ctx, t)
    defer ssh.Close(ctx)
    
    // use ssh.Address() to get host:port
    // default user is "test"
    sshAddr := ssh.Address()
}

// Localstack (S3) test container
func TestWithS3(t *testing.T) {
    ctx := context.Background()
    ls := containers.NewLocalstackTestContainer(ctx, t)
    defer ls.Close(ctx)
    
    s3Client, bucketName := ls.MakeS3Connection(ctx, t)
    
    // put object example
    _, err := s3Client.PutObject(ctx, &s3.PutObjectInput{
        Bucket: aws.String(bucketName),
        Key:    aws.String("test-key"),
        Body:   strings.NewReader("test content"),
    })
    require.NoError(t, err)
}
```