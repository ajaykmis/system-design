"""
WebSocket CLI chat client.

Usage:
    pip install websockets
    python client.py --name alice
    python client.py --name bob
"""

import argparse
import asyncio
import websockets


async def chat(uri: str, name: str):
    async with websockets.connect(f"{uri}?name={name}") as ws:
        print(f"Connected as '{name}'. Type messages below (Ctrl+C to quit).\n")

        async def receiver():
            async for msg in ws:
                print(f"\r{msg}\n> ", end="", flush=True)

        recv_task = asyncio.create_task(receiver())

        loop = asyncio.get_event_loop()
        try:
            while True:
                msg = await loop.run_in_executor(None, lambda: input("> "))
                if msg:
                    await ws.send(msg)
        except (EOFError, KeyboardInterrupt):
            print("\nDisconnected.")
        finally:
            recv_task.cancel()


def main():
    parser = argparse.ArgumentParser(description="WebSocket chat client")
    parser.add_argument("--name", default="anonymous", help="Your display name")
    parser.add_argument("--host", default="localhost", help="Server host")
    parser.add_argument("--port", default=8080, type=int, help="Server port")
    args = parser.parse_args()

    uri = f"ws://{args.host}:{args.port}/ws"
    asyncio.run(chat(uri, args.name))


if __name__ == "__main__":
    main()
