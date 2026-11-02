"""QA TCP forwarder. The QA stack runs on an internal Docker network (no route to the internet), and Docker cannot
publish ports of internal-only containers, so this one container sits on both the internal and an edge network and
relays 8080 -> app:8080 and 9000 -> stub:9000. Nothing else gets an outside route."""

import asyncio

ROUTES = {8080: ("app", 8080), 9000: ("stub", 9000)}


async def pipe(r, w):
    try:
        while data := await r.read(65536):
            w.write(data)
            await w.drain()
    except OSError:
        pass
    finally:
        w.close()


async def handle(cr, cw, target):
    try:
        ur, uw = await asyncio.open_connection(*target)
    except OSError:
        cw.close()
        return
    await asyncio.gather(pipe(cr, uw), pipe(ur, cw))


async def main():
    servers = [
        await asyncio.start_server(lambda r, w, t=t: handle(r, w, t), "0.0.0.0", p)
        for p, t in ROUTES.items()
    ]
    await asyncio.gather(*(s.serve_forever() for s in servers))


asyncio.run(main())
