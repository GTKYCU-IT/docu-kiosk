import http.server
import mimetypes
import os

mimetypes.add_type('application/x-chrome-extension', '.crx')

public = os.path.join(os.path.dirname(os.path.abspath(__file__)), 'public')

class Handler(http.server.SimpleHTTPRequestHandler):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, directory=public, **kwargs)

with http.server.HTTPServer(('0.0.0.0', 8000), Handler) as httpd:
    print(f'Serving {public} on http://0.0.0.0:8000')
    httpd.serve_forever()
