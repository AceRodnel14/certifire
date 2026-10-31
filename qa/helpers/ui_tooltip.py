#!/usr/bin/env python3
"""Tooltip (info icon) behaviour check, visible Chrome, human pace.
usage: uv run --with playwright python -I ui_tooltip.py BASE_URL OUT_DIR USER PASS ADMIN_PASS
Needs a fresh stack with setup done (admin), plus a normal user USER/PASS."""

import sys

from playwright.sync_api import sync_playwright

base, out, user, pw, apw = (
    sys.argv[1].rstrip("/"),
    sys.argv[2],
    sys.argv[3],
    sys.argv[4],
    sys.argv[5],
)
res = []


def rec(ok, n):
    ok = bool(ok)
    res.append(ok)
    print("PASS" if ok else "FAIL", n, flush=True)


def login(pg, u, p):
    pg.goto(base + "/")
    f = pg.locator("form")
    f.get_by_label("Username").first.fill(u)
    f.get_by_label("Password", exact=True).first.fill(p)
    f.locator("button.btn.primary").click()
    pg.wait_for_selector("header.topbar")


def tip(pg, label):
    b = pg.get_by_role("button", name=label)
    return b, pg.locator(f"[id='{b.get_attribute('aria-describedby')}']")


with sync_playwright() as p:
    br = p.chromium.launch(
        executable_path="/usr/bin/google-chrome", headless=False, slow_mo=450
    )
    # ---- touch, 390 px
    c = br.new_context(
        has_touch=True, is_mobile=True, viewport={"width": 390, "height": 800}
    )
    pg = c.new_page()
    pg.set_default_timeout(8000)
    login(pg, user, pw)
    pg.get_by_role("link", name="New certificate").first.click()
    pg.wait_for_selector("#hosts")
    b, t = tip(pg, "About Name")
    w = lambda: pg.wait_for_timeout(800)
    b.tap()
    w()
    rec(t.is_visible(), "touch: first tap opens")
    pg.screenshot(path=out + "/t1_tap_open.png")
    b.tap()
    w()
    rec(t.is_visible(), "touch: second tap keeps it open")
    pg.touchscreen.tap(200, 700)
    w()
    rec(not t.is_visible(), "touch: tap outside closes")
    box = t.bounding_box()
    b.tap()
    w()
    box = t.bounding_box()
    vp = pg.evaluate("innerWidth")
    rec(
        box and box["x"] >= -1 and box["x"] + box["width"] <= vp + 1,
        "touch: tooltip inside 390px viewport",
    )
    pg.touchscreen.tap(200, 700)
    c.close()
    # ---- desktop
    c = br.new_context(no_viewport=True)
    pg = c.new_page()
    pg.set_default_timeout(8000)
    login(pg, user, pw)
    pg.get_by_role("link", name="New certificate").first.click()
    pg.wait_for_selector("#hosts")
    b, t = tip(pg, "About Hosts")
    b.hover()
    w()
    rec(t.is_visible(), "desktop: hover opens")
    b.click()
    w()
    rec(t.is_visible(), "desktop: hover then click stays open")
    pg.screenshot(path=out + "/t2_desktop.png")
    pg.mouse.click(5, 400)
    w()
    rec(not t.is_visible(), "desktop: click outside closes")
    b.focus()
    w()
    rec(t.is_visible(), "desktop: keyboard focus opens")
    pg.keyboard.press("Escape")
    w()
    rec(not t.is_visible(), "desktop: Escape closes")
    b.hover()
    w()
    pg.mouse.move(5, 400)
    w()
    rec(not t.is_visible(), "desktop: mouseleave closes")
    # webhook tooltip
    pg.get_by_role("link", name=user).click()
    pg.wait_for_selector("#hue")
    wb = pg.locator(".tip-btn").filter(has=pg.locator("xpath=.")).first
    wb = pg.get_by_role("button", name="Where to get the URL").first
    wb.scroll_into_view_if_needed()
    wb.hover()
    w()
    wt = pg.locator(f"[id='{wb.get_attribute('aria-describedby')}']")
    rec(
        wt.is_visible() and wt.get_attribute("role") == "tooltip",
        "webhook URL tooltip opens",
    )
    pg.screenshot(path=out + "/t3_webhook_tip.png")
    c.close()
    # admin master key
    c = br.new_context(no_viewport=True)
    pg = c.new_page()
    pg.set_default_timeout(8000)
    login(pg, "admin", apw)
    pg.get_by_role("link", name="Users").click()
    pg.get_by_role("button", name="Reset password").first.click()
    w()
    mb = pg.get_by_role("button", name="About the master key")
    mb.hover()
    w()
    mt = pg.locator(f"[id='{mb.get_attribute('aria-describedby')}']")
    rec(
        mt.is_visible() and mt.get_attribute("role") == "tooltip",
        "admin master-key tooltip opens",
    )
    pg.screenshot(path=out + "/t4_master_key_tip.png")
    c.close()
    br.close()
print(sum(res), "pass", len(res) - sum(res), "fail")
