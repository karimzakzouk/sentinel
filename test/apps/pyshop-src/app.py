"""pyshop is the source of a small Python test service the Fixer fixes the
code of: hack/shop-up.sh pyshop puts this directory in the test Gitea as
person/pyshop-src, and hack/ci.sh, standing in for its pipeline, builds an
image of each commit, labelled with the commit it came from."""

import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import catalog

CATALOG = catalog.load()


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/healthz":
            return self.reply(200, "ok")
        if self.path.startswith("/items/"):
            item = CATALOG.get(self.path[len("/items/"):])
            return self.reply(200, item) if item else self.reply(404, {"error": "no such item"})
        return self.reply(200, {"items": len(CATALOG), "greeting": catalog.greeting()})

    def reply(self, status, body):
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, *args):
        pass


if __name__ == "__main__":
    print(f"pyshop listening on :8080 with {len(CATALOG)} items", flush=True)
    ThreadingHTTPServer(("", 8080), Handler).serve_forever()
