# `full-v2` の最初の実行(2026-11-17)

キャンペーン `full-v2` の最初の実行で、`pat-floor` と `trigger-isolation` の一部が終わった時点で止めた。**この結果の読み方**:

## 確認できたこと(実データ)

1. **一度も使われていない Proxy は、Aurora への接続が 0 本**(`pat-floor`、2 回、各 9 分。`Max`、`Sum`、`Average` のすべて 0)。
2. **ログインだけでは、Aurora への接続は 0 本**(`trigger-isolation` の `connect`、`Sum` = 0)。
3. **`ClientConnections` の `Sum` は、10 ソケットに対して 10**(7 つの run すべて。`Maximum` は 5〜6)。
4. **ピン留めは、クライアント 1 本につき 1 本**(`prepared`: ピン留め `Sum` = 10、`DatabaseConnections` `Sum` = 10)。GET_LOCK も同じ(10 本ピン留め)。
5. **クライアントが閉じたあとも、`DatabaseConnections` の `Sum` は 10**(`getlock-close`: クライアント 0)。

## この結果についての訂正(同日)

最初は、5 を「前の実験の残り」と解釈し、待ちの基準の欠陥だと報告した。**誤りだった。** 後の run で、クライアントのいない 20 分間の待ちを見ると、`Sum` = 10(`Max` = 5)で、**1 時間以上、安定していた**。使われたあとの Proxy は、アイドルの接続を 10 本(設定の `MaxIdleConnectionsPercent` 10% × `max_connections` 100 と一致。2 ノードで 5 本ずつ)保つ、というのが、現在の読み(推定)。詳細: [docs/metrics.md](../../docs/metrics.md)。

そのため、**2〜5 の結果は有効**(5 の「10 本」は、閉じたあとに残るのではなく、アイドルの定常値)。ただし、この run の待ちの基準は `Maximum ≤ 6`(1 ノード分)で、`Sum` では 10 以下という意味なので、**この run の `DatabaseConnections` の値は、`Sum` ≤ 10 を、前の run の影響なしの下限として読む**。10 を超える値だけが、その run の効果。
