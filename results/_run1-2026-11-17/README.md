# `full-v2` の最初の実行(2026-11-17): 待ちの基準の欠陥が見つかった

キャンペーン `full-v2` を、最初の版(run の前に「`DatabaseConnections` の **Maximum** が 6 以下に戻るまで待つ」)で動かし、`pat-floor` と `trigger-isolation` が終わった時点で止めた。**止めた理由と、この結果の読み方**:

## 見つかったこと

1. **未使用の Proxy は、Aurora への接続が 0 本。**(`pat-floor`、2 回、各 9 分、`Max` / `Sum` / `Average` のすべて 0)。前回までの「クライアントがいなくても 5 本は保持する(下限)」は、**常にある下限ではなく、前の実験の残り**だった。
2. **ログインだけでは、Aurora への接続は 0 本。**(`trigger-isolation` の `connect`: `Sum` = 0。前回は、これも「5」だった)
3. **`ClientConnections` の `Sum` は、10 ソケットに対して 10。**(7 つの run すべて。`Maximum` は 5〜6)
4. **ピン留めは、クライアント 1 本につき 1 本。**(`prepared`: ピン留め `Sum` = 10、`DatabaseConnections` `Sum` = 10)。GET_LOCK も同じ(10 本ピン留め、`DatabaseConnections` は 11)。
5. **クライアントが閉じたあとも、裏の接続は残る。**(`getlock-close`: クライアント 0、`DatabaseConnections` の `Sum` = 10)

## なぜ、この結果の 2 番目以降の run を、信頼しないか

待ちの基準が、**Proxy 1 ノード分の `Maximum` と 6 の比較**だった。残りが `Sum` で 10 本あっても、`Maximum` は 5〜6 なので、「戻った」と判定され、**前の run の残りの上で、次の run が始まっていた**。`trigger-isolation` の `ping-loop` 以降の `DatabaseConnections` は、前の run の残りを含みうる(1 番目の `connect` は、`pat-floor` の直後で、残りがない状態から始まっている)。`ClientConnections` と `ピン留め` は、クライアントがいる間の値なので、影響は小さい。

直し方: `lab/exp.py` の `wait_baseline` を、**Proxy 全体(`Sum`)が 2 以下**になるまで待つ形に変えた(`FLOOR_DEFAULT` = 2)。`trigger-isolation` は、この基準で、やり直す。

前回までの実験(元のリポジトリの A1〜A9、B1〜B5)は、同じ欠陥の待ちで測られている。その接続数の結論は、`Sum` と新しい待ちで測り直すまで、暫定。
