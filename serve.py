import asyncio
import http.server
import json
import mimetypes
import os
import threading
import time

import websockets

mimetypes.add_type('application/x-chrome-extension', '.crx')
public = os.path.join(os.path.dirname(os.path.abspath(__file__)), 'public')

# --- WebSocket server (port 8001) ---

connected = set()
ws_loop = None

async def ws_handler(websocket):
    connected.add(websocket)
    print(f'Kiosk connected ({len(connected)} total)')
    try:
        await websocket.wait_closed()
    finally:
        connected.discard(websocket)
        print(f'Kiosk disconnected ({len(connected)} total)')

async def ws_broadcast(url):
    if connected:
        msg = json.dumps({'type': 'sign', 'url': url})
        await asyncio.gather(*[ws.send(msg) for ws in set(connected)])

def broadcast(url):
    if ws_loop:
        asyncio.run_coroutine_threadsafe(ws_broadcast(url), ws_loop)

def start_ws():
    global ws_loop
    ws_loop = asyncio.new_event_loop()
    asyncio.set_event_loop(ws_loop)

    async def run():
        async with websockets.serve(ws_handler, '0.0.0.0', 8001):
            print('WebSocket server on ws://0.0.0.0:8001')
            await asyncio.Future()

    ws_loop.run_until_complete(run())

# --- HTTP server (port 8000) ---

class Handler(http.server.SimpleHTTPRequestHandler):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, directory=public, **kwargs)

    def do_OPTIONS(self):
        self.send_response(200)
        self._cors()
        self.end_headers()

    def do_POST(self):
        if self.path == '/push':
            length = int(self.headers.get('Content-Length', 0))
            body = json.loads(self.rfile.read(length))
            url = body.get('url', '')
            print(f'Push → kiosk {body.get("kioskId")}: {url}')
            broadcast(url)
            self.send_response(200)
            self._cors()
            self.end_headers()
        else:
            self.send_response(404)
            self.end_headers()

    def _cors(self):
        self.send_header('Access-Control-Allow-Origin', '*')
        self.send_header('Access-Control-Allow-Methods', 'POST, OPTIONS')
        self.send_header('Access-Control-Allow-Headers', 'Content-Type')

if __name__ == '__main__':
    threading.Thread(target=start_ws, daemon=True).start()
    time.sleep(0.5)

    httpd = http.server.ThreadingHTTPServer(('0.0.0.0', 8000), Handler)
    print(f'HTTP server on http://0.0.0.0:8000')
    httpd.serve_forever()
