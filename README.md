# 咖啡连锁外卖出杯时效与骑手交接服务

一个基于 **Go 1.19 + PostgreSQL 15 + Redis 7** 的后端服务，把外卖平台订单、门店产能与制作、骑手到店交接、异常协同（门店/店员/骑手/顾客/平台客服/财务）、赔付与门店班组利润复盘串到**同一订单**上。

- 商品覆盖：美式、拿铁、冷萃、果咖、轻食、组合套餐
- 平台覆盖：美团 / 饿了么 / 抖音（多平台订单可合并）
- 提供 **OpenAPI 3.x + 内嵌 Swagger UI**（本项目为后端服务，无业务前端页面）

---

## 一、能力清单（与业务场景对应）

| 业务环节 | 能力 |
|---|---|
| 订单接入 | 订单时间、门店产能、制作配方、冰热属性、糖度、杯型、封签码、预计骑手到店、顾客备注；多平台订单合并 |
| 门店制作 | 按出杯顺序（queue_seq）制作、杯贴打印记录、店员扫描封签后进入待取区、出杯时效 SLA |
| 骑手交接 | 到店打卡；核验订单号/杯数、袋数、冷热分袋、吸管餐具；每次核验留痕（含取错袋） |
| 骑手提前到店 | 饮品未出杯即到店 → 生成等待异常、上骑手到店屏；出杯慢（超 SLA）判定门店责任，客服可发骑手等待补贴 |
| 放置过久重做 | 待取区超时（默认 900s）自动标记并开重做异常；旧杯报损、重做额外扣原料、新杯带新封签重新入队，**重做成本进班组复盘** |
| 备注漏看 / 错杯 | 任一角色可上报；错杯计入班组评分，可触发重做 |
| 轻食缺货 | 制作时缺货 → 订单转异常、商品**临时下架**；补货后手动恢复上架 |
| 平台取消 | 释放未制作杯原料、在制/已出杯报损、冲回收入毛利、记录预期毛利损失、生成平台赔付方案走财务审批 |
| 漏液投诉 | 顾客收货后投诉 → 客服认领、顾客退款+安慰券方案、财务审批/放款、门店承担补偿计入利润 |
| 六方联动 | 每个异常单关联门店、店员、骑手、顾客、平台客服、财务；统一时间线留言，客服/财务留言自动认领 |
| 爆单保护 | 队列超阈值自动限流；店长可手动暂停/恢复接单；暂停期平台推单被拒 |
| 库存 | 接单按配方扣减、重做扣减、取消回补、补货；全部库存流水可追溯 |
| 骑手到店屏 | 实时队列深度、待取区订单、已到店等待骑手、过久杯数、接单状态（Redis 缓存 + 事件流） |
| 区域经理 | 按门店按天看出杯慢/错杯/漏餐具/重做/平台取消对**利润的影响**（重做成本、补偿成本、骑手补贴、取消毛利损失、净利润） |
| 区域督导 | 查看班组评分（异常自动扣分）、写班组复盘；重做成本进入复盘 |

---

## 二、一键启动（Docker Compose）

```bash
docker compose up --build
```

启动后：

- 服务地址：http://localhost:8080
- Swagger UI：http://localhost:8080/swagger/
- OpenAPI JSON：http://localhost:8080/openapi.json
- 健康检查：http://localhost:8080/healthz

容器首次启动会自动执行数据库迁移并写入种子数据（门店、商品配方、库存、账号）。

---

## 三、本地开发启动（不使用 Docker）

需要本地具备 Go 1.19+、PostgreSQL、Redis。

```bash
# 1. 准备数据库（示例）
createdb coffeechain

# 2. 配置环境变量并运行（程序启动时自动建表+种子）
export DATABASE_URL="postgres://用户:密码@127.0.0.1:5432/coffeechain?sslmode=disable"
export REDIS_ADDR="127.0.0.1:6379"
export HTTP_ADDR=":8080"
export PLATFORM_API_KEY="platform-demo-key"

go run ./cmd/server
```

数据库迁移 SQL 内嵌于 `internal/db/migrations/`，幂等可重复执行；种子账号的 bcrypt 密码在首次启动时自动写入。

---

## 四、测试账号

> 密码：管理员 `admin123`，其余所有员工/演示账号均为 `demo123`。

| 角色 | 用户名 | 密码 | 说明 |
|---|---|---|---|
| 平台管理员 | `admin` | admin123 | 全权限 |
| 区域经理 | `manager1` | demo123 | 华东大区，看门店利润影响 |
| 区域督导 | `supervisor1` | demo123 | 班组评分与复盘 |
| 店长 SH001 | `leader1` | demo123 | 南京西路店 |
| 咖啡师 | `barista1` | demo123 | SH001 制作 |
| 打包员 | `packer1` | demo123 | SH001 扫封签/交接 |
| 店长 SH002 | `leader2` | demo123 | 陆家嘴店（轻食初始库存仅 2 份，便于演示缺货下架） |
| 平台客服（美团） | `support1` | demo123 | 异常认领、发起骑手等待补贴 |
| 财务 | `finance1` | demo123 | 赔付审批/放款 |
| 骑手 | 骑手编号 `R001` / `R002` | 免密 | 骑手登录接口 |
| 顾客 | 登录码 `cust1` / `cust2` | 免密 | 顾客登录接口 |

外卖平台接入不走登录，使用请求头 `X-Platform-Key: platform-demo-key`。

门店：`SH001` 南京西路店、`SH002` 陆家嘴店；平台：`meituan` / `eleme` / `douyin`。
商品 SKU：`P001` 美式、`P002` 拿铁、`P003` 冷萃、`P004` 果咖、`P005` 轻食(鸡肉藜麦卷)、`P006` 组合套餐。

---

## 五、核心业务流（curl 速览）

```bash
BASE=http://localhost:8080; KEY=platform-demo-key

# 1) 平台推单（冷热轻食 → 预期 3 袋）
curl -s -X POST $BASE/api/platform/orders -H "X-Platform-Key: $KEY" -H 'Content-Type: application/json' -d '{
  "platform_code":"meituan","platform_order_no":"MT-DEMO-1","store_code":"SH001",
  "customer_code":"cust1","rider_code":"R001",
  "items":[{"sku":"P002","qty":1,"ice_hot":"ice","sugar":"半糖"},
           {"sku":"P001","qty":1,"ice_hot":"hot"},
           {"sku":"P005","qty":1}] }'

# 2) 员工登录
TOK=$(curl -s -X POST $BASE/api/auth/login -H 'Content-Type: application/json' \
  -d '{"username":"packer1","password":"demo123"}' | jq -r .token)

# 3) 看出杯队列 → 开始制作 → 扫封签入待取区
curl -s $BASE/api/stores/SH001/queue -H "Authorization: Bearer $TOK"
curl -s -X POST $BASE/api/cups/1/start -H "Authorization: Bearer $TOK"
curl -s -X POST $BASE/api/cups/seal-scan -H "Authorization: Bearer $TOK" \
  -H 'Content-Type: application/json' -d '{"seal_code":"SL00000001"}'

# 4) 骑手到店屏
curl -s $BASE/api/stores/SH001/board -H "Authorization: Bearer $TOK"

# 5) 交接核验（袋数=3、冷热分袋、吸管餐具齐全）
curl -s -X POST $BASE/api/orders/1/handover -H "Authorization: Bearer $TOK" \
  -H 'Content-Type: application/json' \
  -d '{"bags_actual":3,"hot_cold_split":true,"straw_utensils_ok":true}'

# 6) 顾客投诉漏液 → 客服/财务联动
curl -s -X POST $BASE/api/orders/1/report -H "Authorization: Bearer $TOK" \
  -H 'Content-Type: application/json' -d '{"type":"leak_complaint","detail":"杯底漏液"}'
```

完整六方协同、赔付审批、缺货下架、平台取消、SLA 超时与过久重做场景，见端到端脚本 `scripts/e2e.sh`。

> 演示时间类场景（骑手提前到店、饮品放置过久）：
> `POST /api/demo/clock {"offset_secs":1000}` 快进业务时钟，
> `POST /api/admin/stale-sweep` 立即扫描待取区；`offset_secs=0` 恢复真实时间。

---

## 六、验证方式

### 1. 自动化端到端（推荐，覆盖全部业务场景）

```bash
# 服务启动后执行（默认 BASE_URL=http://127.0.0.1:8080）
BASE_URL=http://127.0.0.1:8080 ./scripts/e2e.sh
```

> 脚本使用固定平台单号，**需在干净数据库上运行一次**（Docker Compose 首次启动即为干净库；
> 本地重跑前重建库即可：`dropdb coffeechain && createdb coffeechain` 后重启服务，迁移与种子会自动重建）。

脚本按 15 个场景顺序验证：六类角色登录 → 多平台接单/合并/封签/袋数 → 出杯与封签 → 骑手提前到店 →
交接四项核验（漏餐具/袋数/冷热分袋）→ 漏液投诉六方联动与财务赔付 → 出杯慢 SLA 与骑手等待补贴 →
放置过久重做 → 备注漏看/错杯再重做 → 轻食缺货临时下架/补货恢复 → 平台取消（释放原料/报损/毛利损失/赔付）→
爆单保护与到店屏 → 区域经理利润影响 → 督导班组评分复盘 → OpenAPI/Swagger。

### 2. 单元测试 / 静态检查

```bash
go test ./...
go vet ./...
```

### 3. 接口文档

浏览器打开 http://localhost:8080/swagger/ 可直接查看并调试全部接口（38 个端点）。

---

## 七、实际验证结论

以下均为本次交付**实际执行**的验证（开发机无 Docker CLI，使用本机 PostgreSQL 15 / Redis 7 独立实例验证服务本体；
Dockerfile 与 docker-compose.yml 已按标准多阶段构建编写）：

- `go vet ./...`：通过。
- `go test ./...`：通过（冷热/轻食分袋袋数、班组时段判定等纯逻辑单测）。
- `scripts/e2e.sh` 全流程：**PASS=98 / FAIL=0**（含正确路径与各类 401/403/404/409/429 拒绝路径）。
- 数据库勾稽抽查：接单按配方扣减库存、重做额外扣减、取消按未制作杯数回补原料；
  在制杯取消按单杯成本报损（1 杯在制拿铁报损 7 元），重做 2 杯报损 16 元；
  四类赔付（顾客退款 59、安慰券 10、骑手等待补贴 6.5、平台取消赔付 24）状态流转正确；
  SH001 当日班组：出杯慢 1、错杯 1、漏餐具 1、重做 2、平台取消 1、漏液 1，评分 100 → 76，
  异常对利润影响合计 105.5 元，净利润随之扣减；区域经理门店日视图与督导复盘均能查到上述结果。
- Swagger UI 与 `/openapi.json`（OpenAPI 3.0.3，38 个端点）可正常加载。

---

## 八、主要目录结构

```
.
├── cmd/server/              # 程序入口（迁移、定时过久扫描、HTTP 启动）
├── internal/
│   ├── api/                 # HTTP 路由/处理器 + 内嵌 openapi.json
│   ├── auth/                # 会话（Redis Token）、bcrypt、鉴权中间件
│   ├── cache/               # Redis 客户端
│   ├── config/              # 环境变量配置
│   ├── db/                  # 数据库连接 + 内嵌迁移 SQL
│   │   └── migrations/      # 0001 表结构 / 0002 种子数据
│   └── service/             # 业务核心：接单/库存/出杯/交接/异常/赔付/复盘/分析
├── scripts/e2e.sh           # 端到端验证脚本
├── Dockerfile               # 多阶段构建
└── docker-compose.yml       # postgres + redis + app 一键启动
```

## 九、数据模型要点

- `orders / order_items / cups`：订单、明细、每杯（唯一 `seal_code` 封签码、`queue_seq` 出杯顺序、重做链 `remake_of_cup_id`）。
- `incidents / incident_threads / compensations`：异常单、六方时间线、赔付方案（提议→批准/驳回→放款）。
- `inventory / stock_movements / stock_losses / temporary_delistings`：库存、流水、报损、临时下架。
- `handover_attempts`：每次骑手交接核验留痕。
- `overload_events`：爆单自动限流与手动暂停/恢复记录。
- `shifts / shift_metrics / shift_reviews`：班组、指标增量累计与评分、督导复盘。
- `v_store_daily_metrics`：区域经理按门店按天的利润影响视图。
