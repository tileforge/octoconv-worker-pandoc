# octoconv-worker-pandoc

Pandoc worker for OctoConv. One process serves one approved manifest over Worker Protocol v2.

The process reads `PANDOC_PROTOCOL_CORE_URL`, `PANDOC_PROTOCOL_CLIENT_CERT`, `PANDOC_PROTOCOL_CLIENT_KEY`, `PANDOC_PROTOCOL_CLIENT_CA`, `PANDOC_PROTOCOL_WORKER_ID`, and `PANDOC_PROTOCOL_MANIFEST_PATH`. It refuses to start when `DATABASE_URL` or `REDIS_ADDR` is set. `PANDOC_WORKER_CONCURRENCY` must be unset or `1`. Hello and pull advertise one slot, and one conversion runs at a time.

`schema/manifest-pandoc-convert.json` is capability `octoconv.convert.pandoc`. It names the 32 pairs in source order. `latex` to `pdf` is not one of them.

`pandoc` must be on `PATH`. The default engine budget is 65s (`PANDOC_ENGINE_TIMEOUT`). `PANDOC_MAX_INPUT_BYTES` defaults to 3145728. The output ceiling is 100 MiB. `--sandbox` is pandoc's own flag. Landlock confines pandoc to the job directory. `PANDOC_SANDBOX_DISABLED=true` turns that off. A sandbox setup failure exits 78. The manifest is not approved.

Assignment parameters are `standalone`, `toc`, `number_sections`, `section_divs`, and `wrap` (`auto`, `none`, or `preserve`). False bools and an empty `wrap` are omitted. Unknown keys and an unknown `wrap` fail before pandoc starts. Structural targets `html`, `latex`, `docx`, and `epub` still get `--standalone` from the converter. The manifest is not approved.

`schema/manifest-pandoc-render.json` is capability `octoconv.render.pandoc` version 1 in a separate file and is not approved: sources md, rst, org, latex, html, docx, and epub each go to png with an empty parameter_schema, render writes HTML with pandoc, lays printed pages out with weasyprint, and fits one page with pdftoppm, a protocol assignment has no size and draws 1280, and direct Convert accepts only size from 256 through 2048.
