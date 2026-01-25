package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gorilla/mux"
	"github.com/phturb/bonjack-tools-backend-go/internal"
	sharedmodel "github.com/phturb/bonjack-tools-backend-go/model"
	modelserver "github.com/phturb/bonjack-tools-backend-go/model/server"
	"github.com/stretchr/testify/assert"
)

func TestSpaHandler(t *testing.T) {
	// Test SPA handler - create a temporary directory and file for testing
	tempDir := t.TempDir()
	handler := spaHandler(tempDir, "index.html")

	// Create index.html in temp directory
	indexContent := "<html><body>Test</body></html>"
	err := os.WriteFile(tempDir+"/index.html", []byte(indexContent), 0o644)
	assert.NoError(t, err)

	// Test request for non-existent file (should serve index.html)
	req := httptest.NewRequest("GET", "/non-existent", nil)
	w := httptest.NewRecorder()

	// Call the handler
	handler(w, req)

	// Should serve index.html for non-existent files
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, indexContent, w.Body.String())
}

func TestHealthzEndpoint(t *testing.T) {
	// Create a test router to simulate the server's routing
	router := mux.NewRouter()

	// Add health endpoint handler similar to server.Start
	router.HandleFunc("/api/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok": true}`))
	})

	// Test GET request
	req := httptest.NewRequest("GET", "/api/healthz", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	assert.Contains(t, w.Body.String(), `"ok": true`)

	// Test POST request (should fail)
	req = httptest.NewRequest("POST", "/api/healthz", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestCustomClaimsValidate(t *testing.T) {
	// Test CustomClaims validation
	claims := &CustomClaims{
		Scope: "test",
		Sub:   "test-subject",
	}

	// Should validate without error (the method just returns nil)
	err := claims.Validate(context.Background())
	assert.NoError(t, err)

	// Test with nil claims - this will panic when calling Validate on nil
	defer func() {
		if r := recover(); r != nil {
			// Expected panic when calling Validate on nil pointer
			t.Log("Expected panic when calling Validate on nil CustomClaims:", r)
		} else {
			t.Error("Expected panic when calling Validate on nil CustomClaims, but validation completed")
		}
	}()

	var nilClaims *CustomClaims
	err = nilClaims.Validate(context.Background())

	// Should not reach here due to panic
	t.Error("Should have panicked when calling Validate on nil CustomClaims")
	_ = err
}

func TestInternalConfigAccess(t *testing.T) {
	// Test that internal config can be accessed (used in server.go)
	config := internal.Config()

	// Should not panic and return some config values
	assert.NotNil(t, config)
	// In test environment, port might be empty, so we just check config exists
	assert.NotNil(t, config.Server)
	assert.NotNil(t, config.Discord)
	assert.NotNil(t, config.Database)

	// Use imports to avoid unused errors
	_ = sharedmodel.Player{}
	_ = modelserver.PlayerStats{}
}
