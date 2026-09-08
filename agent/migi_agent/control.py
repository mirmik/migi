"""Local controls: submit, status, events and explicit stop."""
import argparse
import json
import sys
import urllib.error
import urllib.parse
import urllib.request
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['submit', 'status', 'events', 'stop'])
    parser.add_argument('--thread', required=True)
    parser.add_argument('--run')
    parser.add_argument('--text')
    parser.add_argument('--port', type=int, default=9091)
    args = parser.parse_args()
    if args.action != 'submit' and not args.run:
        parser.error('--run is required')
    run = args.run or uuid.uuid4().hex
    base = f'http://127.0.0.1:{args.port}'
    ids = {'threadId': args.thread, 'runId': run}
    data = None
    if args.action == 'submit':
        text = args.text if args.text is not None else sys.stdin.read()
        data = {**ids, 'messages': [{'id': run, 'role': 'user', 'content': text}]}
        path = '/migi/submit'
    elif args.action == 'stop':
        data = ids
        path = '/agent/cancel'
    else:
        path = '/agent/' + args.action + '?' + urllib.parse.urlencode(ids)
    request = urllib.request.Request(base + path,
        data=None if data is None else json.dumps(data).encode(),
        headers={'Content-Type': 'application/json'})
    try:
        with urllib.request.urlopen(request) as response:
            for line in response:
                print(line.decode().rstrip(), flush=True)
    except urllib.error.HTTPError as exc:
        parser.exit(1, f'HTTP {exc.code}: {exc.read().decode()}\n')
    except urllib.error.URLError as exc:
        parser.exit(1, f'Agent connection failed: {exc.reason}\n')


if __name__ == '__main__':
    main()
