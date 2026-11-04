# ephget-index

Builds the app catalog for [ephget](https://github.com/Dryd33n/ephget). A GitHub
Action runs daily, reads the manifests in five of Scoop's buckets, keeps the apps
ephget can run without Scoop, and publishes the result:

```
https://github.com/Dryd33n/ephget-index/releases/latest/download/index.json.gz
```

The eligibility rules and the schema are in section 6 of ephget's specification.
Manifests are only parsed as JSON, and nothing from a bucket is ever run.

## Running it

It needs Python 3.10+ and Git, with no other dependencies.

```
python -m unittest
python -I build_index.py --update --buckets .cache/scoop --state .cache/state.json --out out
```

This writes three files to `out/`: `index.json.gz`, `index.json.gz.sha256`, and
`report.json`, which isn't published and says why each excluded app was left out.

`--update` clones or fetches the buckets. `--state` keeps each manifest's result
and the bucket's commit, so the next run re-parses only what `git diff` says has
changed. Any change to `build_index.py` causes a full parse, as does a missing
state file or an old commit that's no longer in the clone.

The build fails, and publishes nothing, if the index is over 300 KB, has fewer than
1,000 apps, or has an entry with no URL, hash or exe path.
