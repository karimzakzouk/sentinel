package agents

import "testing"

// TestExtractTriggerEndpoint verifies Layer C — extracting the HTTP endpoint
// that triggered the error from pod logs. v3.14.0.
func TestExtractTriggerEndpoint(t *testing.T) {
        tests := []struct {
                name string
                logs string
                want string
        }{
                {
                        name: "uvicorn access log with query params",
                        logs: `INFO:     172.10.0.1:54321 - "GET /items?index=10 HTTP/1.1" 500
ERROR:    Traceback (most recent call last):
  File "/app/app/main.py", line 66, in get_items
    item = items[index]
IndexError: list index out of range`,
                        want: "/items?index=10",
                },
                {
                        name: "POST request",
                        logs: `POST /api/users HTTP/1.1" 500
TypeError: unsupported operand type`,
                        want: "/api/users",
                },
                {
                        name: "DELETE request",
                        logs: `DELETE /api/orders/123 HTTP/1.1" 500`,
                        want: "/api/orders/123",
                },
                {
                        name: "no HTTP request (non-web service)",
                        logs: `Traceback (most recent call last):
  File "worker.py", line 42, in process_order
    total = price * quantity
TypeError: unsupported operand type(s)`,
                        want: "",
                },
                {
                        name: "multiple requests — returns the FAILING one (v3.15.0 fix)",
                        logs: `GET /health HTTP/1.1" 200
GET /items?index=99 HTTP/1.1" 500
POST /log HTTP/1.1" 200`,
                        want: "/items?index=99",
                },
                {
                        name: "empty logs",
                        logs: "",
                        want: "",
                },
                {
                        name: "PUT with query params",
                        logs: `PUT /api/config?env=prod HTTP/1.1" 500`,
                        want: "/api/config?env=prod",
                },
        }

        for _, tt := range tests {
                t.Run(tt.name, func(t *testing.T) {
                        got := ExtractTriggerEndpoint(tt.logs)
                        if got != tt.want {
                                t.Errorf("ExtractTriggerEndpoint() = %q, want %q", got, tt.want)
                        }
                })
        }
}
