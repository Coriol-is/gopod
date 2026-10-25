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
        # Suppress it because request-derived details include capabilities.
        return None


def local_authority(value, port):
    """Only the loopback/Docker gateway authority for this listener is valid."""
    if len(value) > 512:
        return False
    try:
        parsed = urlsplit("//" + value)
        return (parsed.hostname in {"localhost", "127.0.0.1", "host.docker.internal"}
                and parsed.port == port and not any((parsed.username, parsed.password,
                                                    parsed.path, parsed.query, parsed.fragment)))
    except ValueError:
        return False


def local_origin(value, port):
    if len(value) > 512:
        return False
    try:
        parsed = urlsplit(value)
        return (parsed.scheme == "http" and local_authority(parsed.netloc, port)
                and not any((parsed.path, parsed.query, parsed.fragment)))
    except ValueError:
        return False


def valid_request_id(identity):
    return (identity is None
            or type(identity) is int and abs(identity) <= 9007199254740991
            or isinstance(identity, str) and len(identity) <= 128)


def validate_request(request):
    if (not isinstance(request, dict) or request.get("jsonrpc") != "2.0"
            or set(request) - {"jsonrpc", "id", "method", "params"}):
        raise ValueError("invalid JSON-RPC object")
    method = request.get("method")
    if not isinstance(method, str) or not method or len(method) > 128:
        raise ValueError("invalid method")
    if "id" in request and not valid_request_id(request["id"]):
        raise ValueError("invalid request id")
    params = request.get("params", {})
    if not isinstance(params, dict):
        raise ValueError("invalid params")
    if method == "initialize":
        version = params.get("protocolVersion", "2024-11-05")
        if not isinstance(version, str) or len(version) > 128:
            raise ValueError("invalid protocol version")
    if method == "tools/call":
        if not isinstance(params.get("name"), str) or not isinstance(params.get("arguments"), dict):
            raise ValueError("invalid tool request")
    return request


def response_bytes(payload):
    # JSON remains machine-readable while HTML-significant characters cannot be
    # interpreted as markup even by a client incorrectly treating it as HTML.
    encoded = json.dumps(payload, ensure_ascii=True, allow_nan=False)
    for character, escaped in (("<", r"\u003c"), (">", r"\u003e"), ("&", r"\u0026")):
        encoded = encoded.replace(character, escaped)
    return encoded.encode("utf-8")


class BridgeHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def setup(self):
        super().setup()
        self.connection.settimeout(self.server.request_timeout)

    def log_message(self, _format, *_args):
        # Deliberately suppress BaseHTTPRequestHandler logging: request paths
        # contain bearer capabilities and must never appear in access logs.
        return None

    def send(self, status, payload=None):
        body = b"" if payload is None else response_bytes(payload)
        self.send_response(status)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("X-Content-Type-Options", "nosniff")
        self.send_header("Content-Security-Policy", "default-src 'none'; sandbox")
        self.send_header("Cache-Control", "no-store")
        self.send_header("Connection", "close")
        self.end_headers()
        self.close_connection = True
        if body:
            try:
                self.wfile.write(body)
            except (BrokenPipeError, ConnectionResetError):
                # A disconnected caller cannot receive a response; execution's
                # finally block still releases its lock and removes containers.
                return

    def authorized(self):
        if not self.path.isascii() or not hmac.compare_digest(self.path, self.server.capability_path):
            self.send(404)
            return False
        hosts = self.headers.get_all("Host", [])
        origins = self.headers.get_all("Origin", [])
        valid_host = len(hosts) == 1 and local_authority(hosts[0], self.server.server_port)
        valid_origin = not origins or (len(origins) == 1 and local_origin(origins[0], self.server.server_port))
        if not valid_host or not valid_origin:
            self.send(403)
            return False
        return True

    def body_length(self):
        lengths = self.headers.get_all("Content-Length", [])
        content_types = self.headers.get_all("Content-Type", [])
        if (self.headers.get("Transfer-Encoding") is not None or len(lengths) != 1
                or len(content_types) != 1 or content_types[0].split(";")[0].strip().lower() != "application/json"):
            self.send(400)
            return None
        if not lengths[0].isascii() or not lengths[0].isdecimal():
            self.send(400)
            return None
        length = int(lengths[0])
        if not 0 < length <= REQUEST_LIMIT:
            self.send(413)
            return None
        return length

    def do_POST(self):
        if not self.authorized():
            return
        length = self.body_length()
        if length is None:
            return
        if not self.server.execution_lock.acquire(blocking=False):
            self.send(429)
            return
        try:
            body = self.rfile.read(length)
            if len(body) != length:
                self.send(400)
                return
            request = validate_request(json.loads(body))
            response = dispatch(request, self.server.executor)
            self.send(202 if response is None else 200, response)
        except Exception:
            self.send(400, {"jsonrpc": "2.0", "id": None,
                            "error": {"code": -32602, "message": "Request rejected or execution unavailable"}})
        finally:
            self.server.execution_lock.release()

    def do_GET(self):
        if self.authorized():
            self.send(405)

    do_DELETE = do_GET


def start_bridge(workspace, image, bind="127.0.0.1"):
    if bind not in {"127.0.0.1", "0.0.0.0"}:
        raise ValueError("bridge bind must be loopback or explicit 0.0.0.0")
    executor = Executor(workspace, image)
    server = BridgeServer((bind, 0), BridgeHandler)
    server.executor = executor
    server.capability_path = "/mcp/" + secrets.token_urlsafe(32)
    server.execution_lock = threading.Lock()
    server.thread = threading.Thread(target=server.serve_forever, daemon=True)
    server.thread.start()
    # This short-lived experimental transport is loopback by default. The only
    # wider binding is explicitly selected for same-host Docker bridge probes;
    # it is not a production network endpoint or a credential transport.
    url = f"http://127.0.0.1:{server.server_port}{server.capability_path}"
    return server, url
