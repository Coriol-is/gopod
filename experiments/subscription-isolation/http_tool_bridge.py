"""Experimental capability-protected HTTP transport for the fixed MCP executor.

No CLI/authentication functionality. Endpoint capability is returned in-process,
never logged. Bind 0.0.0.0 only deliberately for a trusted Docker CLI probe.
"""
import hmac
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import secrets
import threading
from urllib.parse import urlsplit

from tool_server import Executor, REQUEST_LIMIT, dispatch


class BridgeServer(ThreadingHTTPServer):
    daemon_threads = True
    block_on_close = False
    max_workers = 8
    request_timeout = 5

    def __init__(self, *args, **kwargs):
        self._admission = threading.BoundedSemaphore(self.max_workers)
        self.worker_condition = threading.Condition()
        self.active_workers = 0
        super().__init__(*args, **kwargs)

    def _release_worker(self):
        with self.worker_condition:
            self._admission.release()
            self.active_workers -= 1
            self.worker_condition.notify_all()

    def process_request(self, request, client_address):
        # Admission precedes thread creation and header/auth parsing. An idle
        # unauthenticated socket cannot allocate unbounded Python workers.
        if not self._admission.acquire(blocking=False):
            self.shutdown_request(request)
            return
        with self.worker_condition:
            self.active_workers += 1
            self.worker_condition.notify_all()
        try:
            super().process_request(request, client_address)
        except Exception:
            self._release_worker()
            self.shutdown_request(request)
            raise

    def process_request_thread(self, request, client_address):
        try:
            super().process_request_thread(request, client_address)
        finally:
            self._release_worker()

    def close(self):
        self.executor.cancel_signal = 15
        self.shutdown()
        # Wait for a running request's Executor finally block to remove its
        # container; shutting the HTTP listener alone does not cancel tools.
        if not self.execution_lock.acquire(timeout=20):
            self.server_close()
            raise RuntimeError("bridge execution did not stop")
        self.execution_lock.release()
        self.server_close()
        self.thread.join(timeout=15)

    def __enter__(self):
        return self

    def __exit__(self, *_args):
        self.close()

    def handle_error(self, _request, _client_address):
        # Default implementation prints request traceback and peer information.
        pass


def start_bridge(workspace, image, bind="127.0.0.1"):
    if bind not in {"127.0.0.1", "0.0.0.0"}:
        raise ValueError("bridge bind must be loopback or explicit 0.0.0.0")
    executor = Executor(workspace, image)
    capability_path = "/mcp/" + secrets.token_urlsafe(32)
    busy = threading.Lock()

    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def setup(self):
            super().setup()
            self.connection.settimeout(self.server.request_timeout)

        def log_message(self, _format, *_args):
            pass

        def send(self, status, payload=None):
            body = b"" if payload is None else json.dumps(payload).encode()
            self.send_response(status)
            self.send_header("Content-Length", str(len(body)))
            self.send_header("Content-Type", "application/json")
            self.send_header("Cache-Control", "no-store")
            self.send_header("Connection", "close")
            self.end_headers()
            self.close_connection = True
            if body:
                try:
                    self.wfile.write(body)
                except (BrokenPipeError, ConnectionResetError):
                    pass

        def authorized(self):
            if not hmac.compare_digest(self.path, capability_path):
                self.send(404)
                return False
            host_values = self.headers.get_all("Host", [])
            origin_values = self.headers.get_all("Origin", [])
            if len(host_values) != 1 or len(origin_values) > 1:
                self.send(403)
                return False
            try:
                host = urlsplit("http://" + host_values[0])
                local_names = {"localhost", "127.0.0.1", "host.docker.internal"}
                if host.hostname not in local_names or host.port != self.server.server_port:
                    raise ValueError("untrusted host")
                if host.username or host.password or host.path or host.query or host.fragment:
                    raise ValueError("invalid host")
                if origin_values:
                    origin = urlsplit(origin_values[0])
                    if (origin.scheme != "http" or origin.hostname not in local_names
                            or origin.port != self.server.server_port
                            or origin.username or origin.password or origin.path
                            or origin.query or origin.fragment):
                        raise ValueError("untrusted origin")
            except ValueError:
                self.send(403)
                return False
            return True

        def do_POST(self):
            if not self.authorized():
                return
            lengths = self.headers.get_all("Content-Length", [])
            content_types = self.headers.get_all("Content-Type", [])
            if (self.headers.get("Transfer-Encoding") is not None or len(lengths) != 1
                    or len(content_types) != 1 or content_types[0].split(";")[0].strip().lower() != "application/json"):
                self.send(400)
                return
            try:
                if not lengths[0].isascii() or not lengths[0].isdecimal():
                    raise ValueError("invalid length")
                length = int(lengths[0])
                if not 0 < length <= REQUEST_LIMIT:
                    self.send(413)
                    return
            except ValueError:
                self.send(400)
                return
            if not busy.acquire(blocking=False):
                self.send(429)
                return
            try:
                body = self.rfile.read(length)
                if len(body) != length:
                    self.send(400)
                    return
                request = json.loads(body)
                if not isinstance(request, dict) or request.get("jsonrpc") != "2.0":
                    raise ValueError("invalid request")
                response = dispatch(request, executor)
                self.send(202 if response is None else 200, response)
            except Exception:
                self.send(400, {"jsonrpc": "2.0", "id": None,
                                "error": {"code": -32602, "message": "Request rejected or execution unavailable"}})
            finally:
                busy.release()

        def do_GET(self):
            if self.authorized():
                self.send(405)

        def do_DELETE(self):
            if self.authorized():
                self.send(405)

    server = BridgeServer((bind, 0), Handler)
    server.executor = executor
    server.execution_lock = busy
    server.thread = threading.Thread(target=server.serve_forever, daemon=True)
    server.thread.start()
    url = f"http://127.0.0.1:{server.server_port}{capability_path}"
    return server, url
