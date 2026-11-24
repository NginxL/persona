# Health intake pages

Synthetic `ActionResult` responses exported from the manager's real intake UI handler at commit
`17ef7de095`. They contain no personal records. Requests use `space=health`, a full-role credential,
`context.locale` (`en` or `zh-Hans`), `context.ui_version` (`4` or `10`) and `context.size="full"`.
Initial states are `{"kind":"water"}` and `{"kind":"protein"}`. The water settings response uses
the returned water state with `action="settings"`.

Version 4 uses a row for today's total; version 10 uses a gauge tile and colored chart series.
`health_intake_ui.py` shifts chart labels to UTC today and derives the one-field protein settings
sample from the water settings response. Other sample actions keep the current page and return a toast;
they do not claim to persist records or settings. `make_demo_data.py` bundles these pages for DemoHub.
