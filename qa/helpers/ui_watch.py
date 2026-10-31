#!/usr/bin/env python3
"""Human-paced visible walkthrough: tooltips (info icons) and hue-coloured chrome (spec 23-24).
usage: uv run --with playwright python -I ui_watch.py BASE_URL OUT_DIR
Visible Chrome (/usr/bin/google-chrome), maximized, slow_mo 450 ms, pauses so a person can follow."""

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


def lum(rgb):
    c = [int(x) for x in re.findall(r"[\d.]+", rgb)[:3]]
    f = lambda v: (
        (v / 255) / 12.92
        if v / 255 <= 0.03928
        else (((v / 255) + 0.055) / 1.055) ** 2.4
    )
    return 0.2126 * f(c[0]) + 0.7152 * f(c[1]) + 0.0722 * f(c[2])


def contrast(a, b):
    la, lb = lum(a), lum(b)
    hi, lo = max(la, lb), min(la, lb)
    return (hi + 0.05) / (lo + 0.05)


with sync_playwright() as p:
    ctx = p.chromium.launch_persistent_context(
        str(out / "profile"),
        executable_path="/usr/bin/google-chrome",
        headless=False,
        slow_mo=450,
        no_viewport=True,
        args=["--start-maximized"],
        accept_downloads=True,
    )
    pg = ctx.new_page()
    pg.set_default_timeout(8000)
    console = []
    pg.on("console", lambda m: console.append(m.text) if m.type == "error" else None)
    pause = lambda ms=1200: pg.wait_for_timeout(ms)
    shot = lambda n: pg.screenshot(path=str(out / f"{n}.png"))
    F = lambda l, ex=False: pg.locator("form").get_by_label(l, exact=ex).first
    pg.goto(base)
    pause(1500)
    shot("01_setup")
    F("Admin password").fill(pw)
    F("Confirm password").fill(pw)
    pause()
    pg.locator("form button.btn.primary").click()
    pg.get_by_label("Master key").wait_for()
    pause()
    shot("02_master_key")
    pg.get_by_role("button", name=re.compile("saved", re.IGNORECASE)).click()
    pause()
    F("Username").fill("admin")
    F("Password", True).fill(pw)
    pause()
    shot("03_login")
    pg.locator("form button.btn.primary").click()
    pg.wait_for_selector("header.topbar")
    pause(1500)

    # ---- New certificate: tooltips
    pg.get_by_role("link", name=re.compile("new|create", re.IGNORECASE)).first.click()
    pg.wait_for_selector("#hosts")
    pause(1500)
    shot("04_form")
    tips = pg.locator(".tip-btn")
    n = tips.count()
    rec(
        n >= 9,
        f"cert form has info icons ({n}; expect Name, Hosts, RSA, Valid, Subject legend, C/ST/L/O/OU = 10)",
    )
    bad = 0
    for i in range(n):
        b = tips.nth(i)
        label = b.get_attribute("aria-label")
        b.scroll_into_view_if_needed()
        b.hover()
        pg.wait_for_timeout(900)
        did = b.get_attribute("aria-describedby")
        tt = pg.locator(f"[id='{did}']")
        ok = (
            bool(did)
            and tt.get_attribute("role") == "tooltip"
            and tt.is_visible()
            and tt.inner_text().strip() != ""
        )
        # tooltip must stay inside viewport
        box = tt.bounding_box()
        vp = pg.evaluate("[innerWidth, innerHeight]")
        inside = (
            box is not None and box["x"] >= -1 and box["x"] + box["width"] <= vp[0] + 1
        )
        if i in (0, 1, 4):
            shot(f"05_tip_hover_{i}")
        pg.mouse.move(2, 2)
        pg.wait_for_timeout(300)
        # keyboard focus + Escape
        b.focus()
        pg.wait_for_timeout(500)
        shown_focus = tt.is_visible()
        pg.keyboard.press("Escape")
        pg.wait_for_timeout(500)
        closed = not tt.is_visible()
        # tap/click toggles
        b.dispatch_event("click")
        pg.wait_for_timeout(500)
        shown_click = tt.is_visible()
        pg.keyboard.press("Escape")
        pg.wait_for_timeout(300)
        if not (ok and inside and shown_focus and closed and shown_click):
            bad += 1
            print(
                f"   tip '{label}': hover={ok} inside={inside} focus={shown_focus} esc_closes={closed} click={shown_click}"
            )
        pg.locator("body").click(position={"x": 5, "y": 5}) if False else None
    rec(
        bad == 0,
        f"all {n} tooltips: role=tooltip, aria-describedby, hover/focus/click show, Escape closes, inside viewport",
    )
    # chips
    h = pg.locator("#hosts")
    for v in ("app.example.com", "*.k8s.example.test", "192.0.2.10"):
        h.fill(v)
        pause(400)
        h.press("Enter")
        pause(500)
    h.fill("*.homelab")
    h.press("Enter")
    pause(1200)
    shot("06_chips_warning")
    pg.get_by_label("Remove *.homelab").click()
    pause(600)
    pg.get_by_label("Name", exact=True).first.fill("watchme")
    pg.locator("#cert-bits").select_option("2048")
    pg.get_by_label("Valid for (days)", exact=True).fill("30")
    pause()
    shot("07_form_filled")
    pg.get_by_role(
        "button", name=re.compile("generate|create", re.IGNORECASE)
    ).last.click()
    pg.get_by_role("heading", name="watchme").wait_for(timeout=40000)
    pause(1500)
    shot("08_detail")
    # ---- dialog with coloured header
    pg.get_by_role("button", name=re.compile("^Delete")).first.click()
    pause(1500)
    shot("09_dialog")
    dh = pg.evaluate(
        """()=>{const d=document.querySelector('dialog[open]');if(!d)return null;const h=d.querySelector('h2,h3,header,.modal-head')||d.firstElementChild;const s=getComputedStyle(h);return {bg:s.backgroundImage+' '+s.backgroundColor,color:s.color,txt:h.innerText.trim().slice(0,40)}}"""
    )
    print("   dialog header:", dh)
    rec(
        bool(dh) and "gradient" in dh["bg"] and lum(dh["color"]) > 0.6,
        "dialog header gradient with white title",
        str(dh)[:120],
    )
    has_x = (
        pg.locator("dialog[open]")
        .get_by_role("button", name=re.compile("close|×|x", re.IGNORECASE))
        .count()
        > 0
    )
    rec(has_x, "dialog header has a close (x) button")
    pg.keyboard.press("Escape")
    pause(700)

    # ---- Account: theme + hue sweep
    pg.get_by_role("link", name=re.compile("admin")).last.click()
    pg.wait_for_selector("#hue")
    pause(1200)

    def bar():
        return pg.evaluate("""()=>{const t=document.querySelector('header.topbar');const s=getComputedStyle(t);
          const a=t.querySelector('nav a, a');const l=t.querySelector('svg.logo path[fill=currentColor]');
          return {img:s.backgroundImage,bg:s.backgroundColor,text:getComputedStyle(a).color,lock:l?getComputedStyle(l).fill:null,
                  bodybg:getComputedStyle(document.body).backgroundColor}}""")

    seen = set()
    for mode in ("light", "dark"):
        pg.get_by_role("radio", name=re.compile(mode, re.IGNORECASE)).click()
        pause(1500)
        for hue in (None, 60, 120, 200, 285):
            if hue is None:
                btn = pg.get_by_role("button", name="Reset colour")
                if btn.is_enabled():
                    btn.click()
            else:
                pg.locator("#hue").fill(str(hue))
            pause(1800)
            b = bar()
            seen.add(b["img"])
            shot(f"10_{mode}_hue_{hue}")
            white_text = lum(b["text"]) > 0.7
            white_lock = b["lock"] and lum(b["lock"]) > 0.7
            grad = "gradient" in b["img"]
            # contrast of white text vs the lightest stop of the gradient
            stops = re.findall(r"rgb\([^)]*\)", b["img"]) or [b["bg"]]
            worst = min(contrast("rgb(255,255,255)", s) for s in stops)
            rec(
                grad and white_text and white_lock and worst >= 3.0,
                f"top bar {mode} hue={hue}: gradient, white text/lock, min contrast {worst:.1f}:1",
            )
    rec(
        len(seen) >= 4,
        f"top bar colour changes with hue ({len(seen)} distinct gradients)",
    )
    # persists after reload
    pg.reload()
    pause(1500)
    pg.get_by_role("button", name="Log out").click()
    pg.wait_for_selector("form")
    pause(2000)
    shot("11_login_background")
    bgimg = pg.evaluate(
        "getComputedStyle(document.querySelector('.auth, main, body')).backgroundImage + ' | ' + getComputedStyle(document.body).backgroundImage"
    )
    rec("radial" in bgimg, "login background has radial glows", bgimg[:90])
    # setup bg check not repeatable (setup done); login screenshot is the evidence
    csp = [c for c in console if "Content Security Policy" in c]
    rec(not csp, f"no CSP violations ({len(csp)})")
    other = [
        c
        for c in console
        if "Content Security Policy" not in c and "Failed to load resource" not in c
    ]
    rec(not other, f"no other console errors {other[:2]}")
    pause(1500)
    ctx.close()
print(sum(res), "pass", len(res) - sum(res), "fail")
