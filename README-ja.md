# SoraQL

SoracomのデータウェアハウスAPIに対してSQLクエリを実行するためのコマンドラインツールです。既存のSoracom CLIプロファイルを使用して、テレメトリデータのクエリ、スキーマ探索、データ分析ワークフローの管理を直感的に行うことができます。

## 機能

- **プロファイルベース認証**: 既存のSoracom CLIプロファイルを使用し、エンドポイントを自動検出
- **インタラクティブSQLシェル**: プロファイル対応プロンプト、コマンド履歴、タブ補完、複数行クエリ対応の高機能readline インターフェース
- **スキーマ探索**: 利用可能なテーブルとその構造を閲覧
- **柔軟な認証**: メール/パスワード認証とAPIキー認証の両方をサポート
- **エクスポート機能**: 複数の出力形式（テーブル、CSV、JSON）でJSONL形式のクエリ結果をダウンロード
- **時間範囲クエリ**: 様々な形式を使用した時間範囲によるデータフィルタリング
- **デバッグモード**: API操作のトラブルシューティング用の詳細ログ

## インストール

### 前提条件

- Go 1.19以降
- 適切なプロファイルで設定されたSoracom CLI

### ソースからビルド

```bash
git clone https://github.com/soracom/soraql.git
cd soraql
go build -o soraql main.go
```

## 設定

SoraQLは既存のSoracom CLIプロファイルを使用します。アクセスする環境用のプロファイルを設定してください：

```bash
# デフォルトプロファイル（本番環境）を設定
soracom configure --profile default
# 選択: 日本またはグローバルカバレッジ、次にオペレーター認証情報
# メール: your-email@soracom.jp（またはAPIキーを使用）
# パスワード: [あなたのパスワード]

# 必要に応じて追加のプロファイルを設定
soracom configure --profile dev
soracom configure --profile staging
```

### プロファイル設定

プロファイルは `~/.soracom/PROFILE.json` に保存され、以下を含みます：

- **認証情報**: メール/パスワード または authKeyId/authKey
- **カバレッジタイプ**: 日本は `"jp"`、グローバルは `"g"`
- **オプション カスタムエンドポイント**: デフォルトエンドポイントを上書き

## 使用方法

### 基本的なクエリ実行

```bash
# デフォルトプロファイルを使用してクエリ
soraql -sql "SELECT * FROM SIM_SESSION_EVENTS LIMIT 10"

# 特定のプロファイルを使用してクエリ
soraql -profile dev -sql "SELECT COUNT(*) FROM CELL_TOWERS"

# 出力形式を指定してクエリ
soraql -format csv -sql "SELECT * FROM SIM_SNAPSHOTS LIMIT 5"
```

### スキーマ探索

利用可能なテーブルのスキーマ情報を取得：

```bash
# 全テーブルを表示
soraql -schema

# 特定のテーブルスキーマを表示
soraql -schema
# 次に使用: .schema SIM_SESSION_EVENTS
```

### 時間範囲クエリ

時間範囲でデータをフィルタリング：

```bash
# 過去24時間
soraql -from "-24h" -to "now" -sql "SELECT * FROM SIM_SESSION_EVENTS"

# 特定の日付範囲
soraql -from "2024-01-01 00:00:00" -to "2024-01-02 00:00:00" -sql "SELECT * FROM CELL_TOWERS"

# Unixタイムスタンプ
soraql -from "1640995200" -to "1641081600" -sql "SELECT * FROM SIM_SNAPSHOTS"
```

### インタラクティブモード

インタラクティブSQLシェルを起動：

```bash
# デフォルトプロファイル
soraql

# 特定のプロファイル
soraql -profile myprofile
```

インタラクティブプロンプトはプロファイル名を表示：
```
myprofile> SELECT COUNT(*) FROM SIM_SNAPSHOTS;
myprofile> .tables
myprofile> .schema SIM_SESSION_EVENTS
myprofile> exit
```

#### インタラクティブ機能:
- **プロファイル対応プロンプト**: 使用しているプロファイル/認証情報を表示
- **コマンド履歴**: 上下矢印キーで操作、セッション間で永続化（`~/.soraql_history`）
- **タブ補完**: SQLキーワード、テーブル名、関数名
- **複数行クエリ**: セミコロンまで自動継続
- **インライン編集**: 完全なカーソル移動と編集機能

#### 特殊コマンド:
- `.tables` - 利用可能な全テーブルを表示
- `.schema [TABLE_NAME]` - テーブルスキーマを表示
- `.window [show|clear|<from> <to>]` - クエリの時間範囲を管理
- `.debug [on|off|show]` - デバッグモードの切り替え
- `.format [table|csv|json|show]` - 出力形式の設定
- `.ask <質問>` - SQLアシスタントにヘルプを求める
- `exit`, `quit`, `\q`, `.exit`, `.quit` - インタラクティブモードを終了

### 出力形式

```bash
# テーブル形式（デフォルト）
soraql -format table -sql "SELECT * FROM SIM_SNAPSHOTS LIMIT 3"

# CSV形式
soraql -format csv -sql "SELECT * FROM SIM_SNAPSHOTS LIMIT 3"

# JSON形式
soraql -format json -sql "SELECT * FROM SIM_SNAPSHOTS LIMIT 3"
```

### デバッグモード

詳細ログを有効化し、結果ファイルを自動的に開く：

```bash
soraql -debug -sql "SELECT COUNT(*) FROM SIM_SNAPSHOTS"
soraql -debug -open -sql "SELECT * FROM SIM_SESSION_EVENTS LIMIT 5"
```

### パイプ入力

標準入力から複数のクエリを処理：

```bash
echo 'SELECT COUNT(*) FROM SIM_SNAPSHOTS' | soraql
printf "query1\nquery2\nexit\n" | soraql -profile myprofile
```

### ヘルプ

使用方法の情報を表示：

```bash
soraql -h
```

## アーキテクチャ

### 認証フロー
1. `~/.soracom/{profile}.json` からプロファイル設定を読み込み
2. `coverageType` とオプションの `endpoint` フィールドに基づいてエンドポイントを決定
3. メール/パスワード または APIキーを使用して `/v1/auth` エンドポイントで認証
5. 取得したトークンを後続のAPI呼び出しで使用

### クエリ実行
1. `/v1/analysis/queries` にクエリを送信（POST）
2. `/v1/analysis/queries/{queryId}` でクエリステータスをポーリング（GET）
3. `/v1/analysis/queries/{queryId}?exportFormat=jsonl` から結果をダウンロード（GET）
4. `/tmp/` ディレクトリから結果を展開して表示

### エラーハンドリング
- **SQLコンパイルエラー**: 無効な列名、構文エラー（ANA0005）
- **パラメータエラー**: 不正なクエリ（ANA0011）
- **HTTPエラー**: ネットワーク問題、認証失敗
- **ファイル処理**: ダウンロードと展開エラーの処理

## 利用可能なテーブル

クエリ可能な共通テーブル：
- `SIM_SESSION_EVENTS`: SIMセッションと接続イベント
- `SIM_SNAPSHOTS`: ポイントインタイムSIMステータス情報
- `CELL_TOWERS`: セルタワーの位置とメタデータ
- `COUNTRIES`: 国情報テーブル
- `HARVEST_DATA`: 収集されたデータテーブル
- `MCC_MNC`: モバイル国/ネットワークコードテーブル

全ての利用可能なテーブルを確認するには、インタラクティブモードで `.tables` を使用するか、`-schema` オプションを使用してください。

## 使用例

### 基本的なクエリ
```bash
# 総SIMセッション数をカウント
soraql -sql "SELECT COUNT(*) FROM SIM_SESSION_EVENTS"

# 最近のSIMスナップショット
soraql -sql "SELECT * FROM SIM_SNAPSHOTS WHERE TIMESTAMP > '2024-01-01' LIMIT 10"

# 特定の国のセルタワー位置
soraql -sql "SELECT * FROM CELL_TOWERS WHERE COUNTRY = 'JP' LIMIT 5"
```

### インタラクティブセッション
```bash
$ soraql -profile production
production> .tables
┌────────────────────────────────────────────┐
│ SIM_SESSION_EVENTS                         │
│ SIM_SNAPSHOTS                             │
│ CELL_TOWERS                               │
│ HARVEST_DATA                              │
└────────────────────────────────────────────┘
(4 tables)

production> SELECT COUNT(*) FROM SIM_SESSION_EVENTS;
┌───────────┐
│ COUNT(*)  │
├───────────┤
│    15432  │
└───────────┘
(1 rows)

production> exit
```

## 貢献

1. リポジトリをフォーク
2. フィーチャーブランチを作成（`git checkout -b feature/amazing-feature`）
3. 変更をコミット（`git commit -m 'Add amazing feature'`）
4. ブランチにプッシュ（`git push origin feature/amazing-feature`）
5. プルリクエストを作成

## ライセンス

このプロジェクトはMITライセンスの下でライセンスされています - 詳細はLICENSEファイルを参照してください。