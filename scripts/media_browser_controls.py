"""Interact with settings through the same in-video menu as a viewer."""
def select_setting(page, selector, **options):
    if not page.locator(selector).is_visible():
        page.locator('#video-stage').hover()
        page.get_by_role('button', name='Настройки плеера', exact=True).click()
    page.select_option(selector, **options)
