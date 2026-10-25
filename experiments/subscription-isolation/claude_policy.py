#!/usr/bin/env python3
"""Generate a Claude 2.1.310 qualification command; never execute it.

This module does not inspect, read, export or copy an authentication store.
The runtime owner must provide a separate, minimal trusted CLI environment.
"""
import argparse
import ipaddress
import json
import uuid
from urllib.parse import urlparse


SETTINGS = {
    "disableClaudeAiConnectors": True,
    "disableAllHooks": True,
    "autoMemoryEnabled": False,
}
ENV_OVERRIDES = {
    "ENABLE_CLAUDEAI_MCP_SERVERS": "false",
    "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
    "DISABLE_AUTOUPDATER": "1",
}


def mcp_config(url):
    """Qualification endpoint only; no external URLs, credentials or query."""
    parsed = urlparse(url)
    try:
        loopback = parsed.hostname == "localhost" or ipaddress.ip_address(parsed.hostname).is_loopback
    except ValueError:
        loopback = False
    if (parsed.scheme != "http" or not loopback or not parsed.port
            or parsed.username or parsed.password or parsed.query or parsed.fragment):
        raise ValueError("use an explicit loopback HTTP MCP endpoint without credentials")
    return {"mcpServers": {"group": {"type": "http", "url": url}}}


def build_command(url, prompt, *, executable="claude", session_id=None, resume=False):
    config = mcp_config(url)
    if not isinstance(prompt, str) or not prompt:
        raise ValueError("synthetic prompt required")
    if session_id is not None:
        uuid.UUID(session_id)
    elif resume:
        raise ValueError("resume requires an explicit session id")
    command = [executable, "--restricted", "--tools", "",
               "--strict-mcp-config", "--mcp-config", json.dumps(config),
               "--settings", json.dumps(SETTINGS), "--setting-sources", "",
               "--disable-slash-commands", "--permission-mode", "dontAsk",
               "--allowedTools", "mcp__group__group_exec",
               "--system-prompt", "You are a synthetic isolation qualification assistant. Use only the explicitly supplied group tool.",
               "--system-prompt-snapshot", "off",
               "--output-format", "stream-json", "--verbose"]
    if session_id is None:
        command += ["--no-session-persistence"]
    else:
        command += ["--resume" if resume else "--session-id", session_id]
    return command + ["-p", prompt]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mcp-url", required=True)
    parser.add_argument("--prompt", default="Use group_exec to run printf qualification-ok.")
    parser.add_argument("--session-id")
    parser.add_argument("--resume", action="store_true")
    arguments = parser.parse_args()
    command = build_command(arguments.mcp_url, arguments.prompt,
                            session_id=arguments.session_id, resume=arguments.resume)
    print(json.dumps({"argv": command, "env_overrides": ENV_OVERRIDES,
                      "executes": False, "requires": ["isolated trusted CLI runtime", "synthetic working directory", "controller-owned config and fixed MCP", "no inherited personal context"]}, indent=2))


if __name__ == "__main__":
    main()
