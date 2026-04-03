# AMQP HTTP Gateway 実装計画書

## 概要

RabbitMQへのAMQP通信をHTTP経由で中継するgatewayを構築する。ネットワーク的にRabbitMQへ直接接続できないクライアントが、HTTPでメッセージをpublishできるようにする。consume等の機能は意図的にスコープ外とする。

言語: Go

## API仕様

### エンドポイント

| メソッド | パス | 用途 | レスポンス |
|---|---|---|---|
| `POST` | `/v1/publish` | fire-and-forget publish | `202 Accepted` |
| `POST` | `/v1/rpc` | RPC（応答待ち） | `200` + 応答body |
| `GET` | `/healthz` | サービス生存確認 | `200` |
| `GET` | `/readyz` | RabbitMQ接続確認 | `200` / `503` |

### 認証

- HTTP Basic認証（`Authorization: Basic ...`）
- credentialをそのままRabbitMQ接続に使用
- 認証失敗 → `401 Unauthorized`

### リクエスト仕様

#### HTTPボディ

HTTPリクエストボディがそのままAMQPメッセージのbodyになる。エンコーディングやラッピングは行わない。

#### AMQPヘッダー

| HTTPヘッダー | AMQPフィールド | デフォルト | 備考 |
|---|---|---|---|
| `AMQP-Exchange` | exchange | `""` (default exchange) | |
| `AMQP-Routing-Key` | routing key | `""` | |
| `AMQP-VHost` | vhost | `/` | |
| `AMQP-Delivery-Mode` | delivery_mode | `2` (persistent) | |
| `AMQP-Message-Id` | message_id | − | |
| `AMQP-Correlation-Id` | correlation_id | RPCでは自動生成 | |
| `AMQP-Expiration` | expiration | − | |
| `AMQP-Mandatory` | mandatory flag | `false` | |
| `AMQP-Timeout` | − | `30000` | RPCのみ。応答待ちタイムアウト(ms) |
| `AMQP-Header-*` | headers table | − | プレフィックスを除去してheaders tableに展開 |
| `Content-Type` | content_type | − | HTTP標準ヘッダーをそのまま流用 |

### レスポンス仕様

#### publish (`/v1/publish`)

- `202 Accepted` — publisher confirmが返った
- レスポンスボディは空

#### rpc (`/v1/rpc`)

- `200 OK` — 応答メッセージのbodyをHTTPレスポンスボディとして返却
- 応答メッセージのAMQPプロパティを `AMQP-*` レスポンスヘッダーで返却
- `Content-Type` は応答メッセージのcontent_typeをそのまま使用

#### エラー

| コード | 意味 |
|---|---|
| `401` | RabbitMQ認証失敗 |
| `400` | バリデーションエラー |
| `403` | exchangeへの権限なし（RabbitMQ ACL） |
| `404` | exchangeが存在しない |
| `504` | RPCタイムアウト |
| `503` | RabbitMQ接続不可 |

### RPC内部フロー

1. 一時queue(exclusive, auto-delete)を作成
2. `reply_to` に一時queue名、`correlation_id` をセット（明示指定がなければ自動生成）
3. メッセージをpublish
4. 一時queueからconsumeして応答を待つ
5. 応答到着 → `200` でbodyを返却
6. `AMQP-Timeout` 超過 → `504 Gateway Timeout`
7. 一時queueを削除（defer）

## 実装方針

### コネクション管理

- リクエストごとにAMQP接続を作成し、処理完了後に切断する
- 流量が少ない（数リクエスト/分）ためプーリングは不要
- 将来的に流量が増えた場合はuser単位でのコネクションプールを検討

### ライブラリ

- HTTPサーバー: 標準 `net/http`
- AMQPクライアント: `github.com/rabbitmq/amqp091-go`
- CLI: `github.com/alecthomas/kong`
- 設定ファイル: `github.com/fujiwara/jsonnet-armed`（Jsonnet/JSON形式）
- ログ: 標準 `log/slog`

### 設定

Jsonnet/JSON形式のconfigファイルで以下を設定:

```jsonnet
{
  rabbitmq_url: "amqp://localhost:5672",
  listen_addr: ":8080",  // デフォルト ":8080"
}
```

- `rabbitmq_url` — RabbitMQ URL（vhost以外の接続先。vhostはリクエストヘッダーで指定）
- `listen_addr` — HTTPリッスンアドレス

認証情報はリクエストのBasic認証から取得するため、サーバー側設定には含めない。
configファイルパスはCLIフラグ `-c` / `--config` または環境変数で指定する。

### ヘッダーパース処理

1. リクエストヘッダーから `AMQP-` プレフィックスのものを抽出
2. 各ヘッダーを対応するAMQPフィールドにマッピング
3. `AMQP-Header-*` はプレフィックスを除去してAMQP headers tableに展開
4. 未知の `AMQP-*` ヘッダーは無視（将来の拡張性のため）

## スコープ外

以下は意図的にスコープ外とする:

- consume / subscribe
- queue宣言 / binding管理
- RabbitMQ Management APIの機能
- コネクションプーリング（必要になったら追加）
- TLS終端（リバースプロキシ側で処理する想定）
