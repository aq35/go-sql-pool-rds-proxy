# campaign opt-v1

最適化した統合キャンペーン(約 10 時間)。順番: A 遅延と CPU の実験(隙間なし)→ B Proxy の設定を変える実験 → C Proxy の裏の接続数に依存する実験(残りが少ない順)→ D writer の再起動(最後)。観測は、実験ごとに最後に 1 回(collect_later)。独立な条件は 1 run に同時に(idle-gap-matrix)。trigger-isolation は、full-v2 で完了済みなので含めない。pat-warmup は、前回の結果があり、冷えた Proxy は、もう作れないので含めない

- [exceed-cases__db.r6g.large](exceed-cases__db.r6g.large/summary.md)
- [idle-sweep__db.r6g.large](idle-sweep__db.r6g.large/summary.md)
- [pat-contention__db.r6g.large](pat-contention__db.r6g.large/summary.md)
- [pat-control__db.r6g.large](pat-control__db.r6g.large/summary.md)
- [pat-driver-ab__db.r6g.large](pat-driver-ab__db.r6g.large/summary.md)
- [pat-idle-gap__db.r6g.large](pat-idle-gap__db.r6g.large/summary.md)
- [pat-limit__db.r6g.large](pat-limit__db.r6g.large/summary.md)
- [pat-mixed__db.r6g.large](pat-mixed__db.r6g.large/summary.md)
- [pat-pin__db.r6g.large](pat-pin__db.r6g.large/summary.md)
- [pat-reboot__db.r6g.large](pat-reboot__db.r6g.large/summary.md)
- [pat-serial-parallel__db.r6g.large](pat-serial-parallel__db.r6g.large/summary.md)
- [pat-stampede__db.r6g.large](pat-stampede__db.r6g.large/summary.md)
- [pat-timeout__db.r6g.large](pat-timeout__db.r6g.large/summary.md)
- [pinning-impact__db.r6g.large](pinning-impact__db.r6g.large/summary.md)
- [proxy-vs-direct__db.r6g.large](proxy-vs-direct__db.r6g.large/summary.md)
