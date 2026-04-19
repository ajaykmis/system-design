"""
WebSocket chat server in Python.

Usage:
    pip install websockets
    python server.py

Then open http://localhost:8080 in a browser, or use client.py.
"""

import asyncio
import http
import urllib.parse
import websockets

CLIENTS: dict[websockets.WebSocketServerProtocol, str] = {}


async def broadcast(message: str, exclude=None):
    """Send message to all clients except the excluded one."""
    for ws, _ in list(CLIENTS.items()):
        if ws is exclude:
            continue
        try:
            await ws.send(message)
        except websockets.ConnectionClosed:
            pass


async def handler(ws: websockets.WebSocketServerProtocol):
    # Extract username from query string
    params = urllib.parse.parse_qs(urllib.parse.urlparse(ws.path).query)
    name = params.get("name", ["anonymous"])[0]

    CLIENTS[ws] = name
    print(f"+ {name} connected ({len(CLIENTS)} online)")
    await broadcast(f"[{name} joined the chat]", exclude=ws)

    try:
        async for msg in ws:
            formatted = f"{name}: {msg}"
            print(formatted)
            await broadcast(formatted, exclude=ws)
    except websockets.ConnectionClosed:
        pass
    finally:
        del CLIENTS[ws]
        print(f"- {name} disconnected ({len(CLIENTS)} online)")
        await broadcast(f"[{name} left the chat]")


HTML_PAGE = """<!DOCTYPE html>
<html>
<head>
  <title>WebSocket Chat</title>
  <style>
    body { font-family: monospace; max-width: 600px; margin: 40px auto; }
    #log { height: 300px; overflow-y: scroll; border: 1px solid #ccc; padding: 8px; background: #111; color: #0f0; }
    input { width: 70%%; padding: 6px; } button { padding: 6px 16px; }
  </style>
</head>
<body>
  <h2>WebSocket Chat Room</h2>
  <div id="log"></div>
  <br>
  <input id="msg" placeholder="Type a message..." onkeydown="if(event.key==='Enter')send()">
  <button onclick="send()">Send</button>

  <script>
    const name = prompt("Enter your name:") || "anonymous";
    const ws = new WebSocket("ws://" + location.host + "/ws?name=" + encodeURIComponent(name));
    const log = document.getElementById("log");

    ws.onopen    = () => appendLog("Connected as " + name);
    ws.onmessage = (e) => appendLog(e.data);
    ws.onclose   = () => appendLog("Disconnected");

    function appendLog(msg) {
      log.innerHTML += msg + "\\n";
      log.scrollTop = log.scrollHeight;
    }
    function send() {
      const input = document.getElementById("msg");
      if (input.value) { ws.send(input.value); appendLog("you: " + input.value); input.value = ""; }
    }
  </script>
</body>
</html>"""


async def serve_http(path, request_headers):
    """Serve HTML page for non-WebSocket requests."""
    if path == "/" or path == "":
        return http.HTTPStatus.OK, [("Content-Type", "text/html")], HTML_PAGE.encode()
    return None  # let websockets handle /ws


async def main():
    async with websockets.serve(
        handler,
        "localhost",
        8080,
        process_request=serve_http,
    ):
        print("WebSocket server running on http://localhost:8080")
        print("Open the URL in a browser or run: python client.py --name alice")
        await asyncio.Future()  # run forever


if __name__ == "__main__":
    asyncio.run(main())
