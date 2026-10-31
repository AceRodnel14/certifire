#!/usr/bin/env python3
"""Focused UI re-check: delete dialog (list/detail/failure), logo in light/dark + hues, favicon under emulated color schemes.
usage: uv run --with playwright python -I ui_recheck.py BASE_URL OUT_DIR  (fresh stack, visible Chrome)"""

import pathlib
import re
import secrets
import sys

from playwright.sync_api import sync_playwright

base, out = sys.argv[1].rstrip("/"), pathlib.Path(sys.argv[2])
out.mkdir(parents=True, exist_ok=True)
pw = "qa-" + secrets.token_hex(6)
res = []


def rec(ok, name, extra=""):
    res.append(ok)
    print("PASS" if ok else "FAIL", name, extra, flush=True)


def dlg(pg):
    return pg.evaluate("!!document.querySelector('dialog[open]')")


with sync_playwright() as p:
    ctx = p.chromium.launch_persistent_context(
        str(out / "profile"),
        executable_path="/usr/bin/google-chrome",
        headless=False,
        slow_mo=120,
        viewport={"width": 1200, "height": 800},
        color_scheme="light",
    )
    pg = ctx.new_page()
    pg.set_default_timeout(8000)
    pg.goto(base)
    F = lambda l, ex=False: pg.locator("form").get_by_label(l, exact=ex).first
    pg.wait_for_selector("svg.logo")
    pg.screenshot(path=str(out / "setup_light.png"))
    rec(
        pg.locator("svg.logo").count() >= 1,
        f"setup card shows logo (count={pg.locator('svg.logo').count()})",
    )

    def lockfill():
        return pg.evaluate(
            "getComputedStyle(document.querySelector('svg.logo path[fill=currentColor]')).fill"
        )

    pg.emulate_media(color_scheme="dark")
    pg.wait_for_timeout(400)
    pg.screenshot(path=str(out / "setup_dark.png"))
    print("  lock fill (os dark):", lockfill())
    pg.emulate_media(color_scheme="light")
    F("Admin password").fill(pw)
    F("Confirm password").fill(pw)
    pg.locator("form button.btn.primary").click()
    pg.get_by_label("Master key").wait_for()
    pg.get_by_role("button", name=re.compile("saved", re.IGNORECASE)).click()
    F("Username").fill("admin")
    F("Password", True).fill(pw)
    pg.screenshot(path=str(out / "login_light.png"))
    pg.locator("form button.btn.primary").click()
    pg.wait_for_selector("header svg.logo, nav, .brand")
    rgb = lambda s: tuple(int(x) for x in re.findall(r"\d+", s)[:3])
    for mode in ("light", "dark"):
        pg.goto(base + "/#/account")
        pg.get_by_role("radio", name=re.compile(mode, re.IGNORECASE)).click()
        pg.wait_for_timeout(500)
        for hue in (None, 30, 200, 300):
            if hue is None:
                pg.get_by_role("button", name="Reset colour").click() if pg.get_by_role(
                    "button", name="Reset colour"
                ).is_enabled() else None
            else:
                pg.locator("#hue").fill(str(hue))
            pg.wait_for_timeout(400)
            pg.screenshot(path=str(out / f"header_{mode}_hue{hue}.png"))
        c = rgb(lockfill())
        lum = sum(c) / 3
        rec(
            (lum < 80) if mode == "light" else (lum > 175),
            f"lock colour in {mode} theme = {c}",
        )
        flame = pg.evaluate(
            "getComputedStyle(document.querySelector('svg.logo path')).fill"
        )
        print("  flame fill:", flame)
    # delete flows
    for n in ("one", "two", "three", "four"):
        pg.evaluate(
            "async n=>{await fetch('/api/certs',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:n,hosts:[n+'.example.com'],bits:2048,days:30,subject:{}})})}",
            n,
        )
    pg.goto(base + "/#/")
    pg.reload()
    pg.wait_for_selector("tbody tr")
    n0 = pg.locator("tbody tr").count()
    pg.get_by_role("button", name=re.compile("^Delete ")).first.click()
    pg.locator("dialog[open] button.danger").click()
    pg.wait_for_timeout(1500)
    rec(
        not dlg(pg) and pg.locator("tbody tr").count() == n0 - 1,
        "list: dialog closes after delete",
        f"rows {n0}->{pg.locator('tbody tr').count()}",
    )
    pg.screenshot(path=str(out / "delete_list_after.png"))
    pg.get_by_role("link", name="Open").first.click()
    pg.wait_for_selector("#file-panel")
    pg.get_by_role("button", name=re.compile("^Delete")).first.click()
    pg.locator("dialog[open] button.danger").click()
    pg.wait_for_timeout(1500)
    rec(
        not dlg(pg) and "certs" not in pg.url.split("#")[-1][:0],
        "detail: dialog closed after delete",
        "url=" + pg.url.split("#")[-1],
    )
    pg.screenshot(path=str(out / "delete_detail_after.png"))
    # forced failure on list
    pg.goto(base + "/#/")
    pg.reload()
    pg.wait_for_selector("tbody tr")
    n1 = pg.locator("tbody tr").count()
    pg.route(
        "**/api/certs/*",
        lambda r: (
            r.fulfill(
                status=500,
                content_type="application/json",
                body='{"error":"forced failure"}',
            )
            if r.request.method == "DELETE"
            else r.continue_()
        ),
    )
    pg.get_by_role("button", name=re.compile("^Delete ")).first.click()
    pg.locator("dialog[open] button.danger").click()
    pg.wait_for_timeout(1200)
    shown = pg.get_by_text("forced failure").count() > 0
    rec(
        dlg(pg) and shown and pg.locator("tbody tr").count() == n1,
        "failure: dialog stays open with error, row kept",
        f"dialog={dlg(pg)} err={shown}",
    )
    pg.screenshot(path=str(out / "delete_failure.png"))
    pg.keyboard.press("Escape")
    pg.unroute("**/api/certs/*")
    # favicon under emulated schemes (rendered standalone) + served
    for cs in ("light", "dark"):
        fp = ctx.new_page()
        fp.emulate_media(color_scheme=cs)
        fp.goto(base + "/favicon.svg")
        fp.wait_for_timeout(300)
        fp.screenshot(path=str(out / f"favicon_{cs}.png"))
        fill = fp.evaluate(
            "(()=>{const e=document.querySelector('path.l, .l')||document.querySelector('path[class]');return e?getComputedStyle(e).fill:null})()"
        )
        c = rgb(fill) if fill else None
        rec(
            c is not None
            and ((sum(c) / 3 < 80) if cs == "light" else (sum(c) / 3 > 175)),
            f"favicon.svg lock {cs}: {fill}",
        )
        fp.close()
    print(
        "tab icon href:", pg.evaluate("document.querySelector('link[rel~=icon]').href")
    )
    ctx.close()
print(sum(res), "pass", len(res) - sum(res), "fail")
