# octoconv-worker-pandoc

Pandoc worker for OctoConv. One process serves one approved manifest over Worker Protocol v1.

The process reads `PANDOC_PROTOCOL_CORE_URL`, `PANDOC_PROTOCOL_CLIENT_CERT`, `PANDOC_PROTOCOL_CLIENT_KEY`, `PANDOC_PROTOCOL_CLIENT_CA`, `PANDOC_PROTOCOL_WORKER_ID`, and `PANDOC_PROTOCOL_MANIFEST_PATH`. It refuses to start when `DATABASE_URL` or `REDIS_ADDR` is set. `PANDOC_WORKER_CONCURRENCY` must be unset or `1`. Hello and pull advertise one slot, and one conversion runs at a time.

`schema/manifest-pandoc-convert.json` is capability `octoconv.convert.pandoc`. It names the 32 pairs in source order. `latex` to `pdf` is not one of them.

`pandoc` must be on `PATH`. The default engine budget is 65s (`PANDOC_ENGINE_TIMEOUT`). `PANDOC_MAX_INPUT_BYTES` defaults to 3145728. The output ceiling is 100 MiB. `--sandbox` is pandoc's own flag. This process does not apply Landlock. Do not move pandoc jobs onto this process until that confinement is measured.

A non-empty options map fails before any program runs. That includes `standalone`, `toc`, `number_sections`, or `section_divs` set to true, and `wrap` set to `auto`, `none`, or `preserve`. An empty options map is accepted. Structural targets `html`, `latex`, `docx`, and `epub` still get `--standalone` from the converter.
