# local-4-1: by-size routing check (step 4.1)

- `QUEUE_LAYOUT=by-size` on the local Docker Compose stack.
- The local catalog (`datasets/local/datasets.csv`) is all small at the real 30 s threshold, so this run pointed the API at a copy of `config/estimates.json` with `small_threshold_s: 2` (a Compose override file outside the repo, setting `ESTIMATES_PATH`). That makes `dc` (~3.3 s predicted) large and `pts-1k` / `polys-10k` (~1 s) small.
- `trace.csv`: T-mixed, seed 11, 1 job/s for 60 s, generated with the same lowered-threshold estimates.
- `queue-counts.txt`: messages left in each queue after the replay (no worker reads `jobs-small` or `jobs-large` until 4.2). jobs-large = 6 = the 6 `dc` jobs in `trace.csv`; jobs-small = the other 48; jobs = 0.
