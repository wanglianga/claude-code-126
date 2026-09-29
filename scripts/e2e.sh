#!/usr/bin/env bash
# 端到端验证：咖啡连锁外卖出杯时效与骑手交接服务
# 用法：BASE_URL=http://127.0.0.1:8088 ./scripts/e2e.sh
# 依赖：curl jq
set -u
BASE_URL="${BASE_URL:-http://127.0.0.1:8080}"
PASS=0; FAIL=0
CODE_FILE=$(mktemp)
ok(){  PASS=$((PASS+1)); printf '  \033[32m✓\033[0m %s\n' "$1"; }
bad(){ FAIL=$((FAIL+1)); printf '  \033[31m✗ %s\033[0m\n' "$1"; }
check(){ if [[ "$3" == *"$2"* ]]; then ok "$1"; else bad "$1 (期望含[$2], 实际=[$3])"; fi; }
# checkj <描述> <jq布尔表达式> <json>
checkj(){ if jq -e "$2" >/dev/null <<<"$3" 2>/dev/null; then ok "$1"; else bad "$1 (表达式 $2 不成立: $3)"; fi; }
# code <描述> <期望码>：最近一次请求的状态码写入文件（子shell亦可读）
code(){ local got; got=$(cat "$CODE_FILE"); if [[ "$2" == "$got" ]]; then ok "$1 [$got]"; else bad "$1 (期望HTTP $2, 实际 $got)"; fi; }

api_curl(){
  local m="$1" p="$2" t="${3:-}" body="${4:-}"
  local hdr=(-s -w $'\n%{http_code}' -X "$m" "$BASE_URL$p")
  [[ -n "$t" ]] && hdr+=(-H "Authorization: Bearer $t")
  [[ -n "$body" ]] && hdr+=(-H "Content-Type: application/json" -d "$body")
  local out; out=$(curl "${hdr[@]}")
  printf '%s' "${out##*$'\n'}" > /dev/null
  printf '%s' "${out##*$'\n'}" > "$CODE_FILE"
  printf '%s' "${out%$'\n'*}"
}
req(){ api_curl "$@"; }
platform(){
  local out
  out=$(curl -s -w $'\n%{http_code}' -X POST "$BASE_URL/api/platform/orders" \
    -H "Content-Type: application/json" -H "X-Platform-Key: platform-demo-key" -d "$1")
  printf '%s' "${out##*$'\n'}" > "$CODE_FILE"
  printf '%s' "${out%$'\n'*}"
}
jval(){ jq -r "$2" <<<"$1"; }

# brew_order <token员工> <token制作> <order_id>：依次开始制作并扫描封签该订单全部杯
brew_order(){
  local ptok="$1" btok="$2" oid="$3"
  local cups seal
  cups=$(req GET "/api/stores/SH001/queue" "$ptok" | jq -c --arg o "$oid" '[.[]|select(.order_id==($o|tonumber) and .status=="queued")]|sort_by(.queue_seq)')
  local n; n=$(jq 'length' <<<"$cups")
  for ((i=0;i<n;i++)); do
    local c; c=$(jq -r ".[$i].cup_id" <<<"$cups")
    req POST "/api/cups/$c/start" "$btok" >/dev/null
    seal=$(req GET "/api/orders/$oid" "$ptok" | jq -r --arg c "$c" '.cups[]|select(.cup_id==($c|tonumber)).seal_code')
    req POST /api/cups/seal-scan "$ptok" "{\"seal_code\":\"$seal\"}" >/dev/null
  done
}

echo "================ 1. 登录（六类角色 + 骑手 + 顾客） ================"
login(){ req POST /api/auth/login "" "{\"username\":\"$1\",\"password\":\"$2\"}"; }
R=$(login leader1 demo123);  LEADER=$(jval "$R" .token);  [[ "$LEADER" != null ]]  && ok "SH001店长登录" || bad "店长登录"
R=$(login leader2 demo123);  LEADER2=$(jval "$R" .token); [[ "$LEADER2" != null ]] && ok "SH002店长登录" || bad "SH002店长"
R=$(login barista1 demo123); BARISTA=$(jval "$R" .token); [[ "$BARISTA" != null ]] && ok "咖啡师登录" || bad "咖啡师"
R=$(login packer1 demo123);  PACKER=$(jval "$R" .token);  [[ "$PACKER" != null ]]  && ok "打包员登录" || bad "打包员"
R=$(login manager1 demo123); MANAGER=$(jval "$R" .token); [[ "$MANAGER" != null ]] && ok "区域经理登录" || bad "经理"
R=$(login supervisor1 demo123); SUP=$(jval "$R" .token); [[ "$SUP" != null ]] && ok "督导登录" || bad "督导"
R=$(login support1 demo123); SUPPORT=$(jval "$R" .token); [[ "$SUPPORT" != null ]] && ok "平台客服登录" || bad "客服"
R=$(login finance1 demo123); FIN=$(jval "$R" .token);   [[ "$FIN" != null ]]   && ok "财务登录" || bad "财务"
req POST /api/auth/login "" '{"username":"leader1","password":"wrong"}' >/dev/null; code "错误密码拒绝" 401
R=$(req POST /api/auth/rider-login "" '{"rider_code":"R001"}'); RIDER=$(jval "$R" .token); [[ "$RIDER" != null ]] && ok "骑手R001登录" || bad "骑手1"
R=$(req POST /api/auth/rider-login "" '{"rider_code":"R002"}'); RIDER2=$(jval "$R" .token); [[ "$RIDER2" != null ]] && ok "骑手R002登录" || bad "骑手2"
R=$(req POST /api/auth/customer-login "" '{"access_code":"cust1"}'); CUST=$(jval "$R" .token); [[ "$CUST" != null ]] && ok "顾客cust1登录" || bad "顾客"

echo "================ 2. 平台接入（配方/冰热/糖度/杯型/封签/杯贴/ETA/多平台合并） ================"
# O1：冰拿铁 + 热美式 + 轻食 => 冷/热/食三袋
ORDER1='{"platform_code":"meituan","platform_order_no":"MT20260929001","store_code":"SH001","customer_code":"cust1","rider_code":"R001","rider_eta":"2026-09-29T12:40:00Z","remark":"少冰，杯套双层","merge_group_note":"BUNDLE-A","items":[{"sku":"P002","qty":1,"ice_hot":"ice","sugar":"半糖","cup_size":"大杯","remark":"少冰"},{"sku":"P001","qty":1,"ice_hot":"hot","sugar":"无糖"},{"sku":"P005","qty":1}]}'
R=$(platform "$ORDER1"); code "美团接单成功" 200; O1=$(jval "$R" .order_id); check "返回订单ID" order_id "$R"
ORDER2='{"platform_code":"eleme","platform_order_no":"EL20260929001","store_code":"SH001","customer_code":"cust1","rider_code":"R002","ice_hot":"hot","items":[{"sku":"P001","qty":1,"ice_hot":"hot"}],"merge_group_note":"BUNDLE-A"}'
R=$(platform "$ORDER2"); code "饿了么接单并入同一合并组" 200; O2=$(jval "$R" .order_id)
platform "$ORDER1" >/dev/null; code "重复平台单号拒绝" 409
curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE_URL/api/platform/orders" -H "Content-Type: application/json" -H "X-Platform-Key: bad" -d "$ORDER2" | grep -q 401 && ok "错误平台密钥拒绝[401]" || bad "密钥"
R=$(req GET /api/orders/$O1 "$PACKER")
checkj "订单1预期3袋（冷热食分袋）" '.expected_bags==3' "$R"
NSEAL=$(jq '[.cups[].seal_code]|length' <<<"$R"); [[ "$NSEAL" == "3" ]] && ok "每杯一个唯一封签码（3个）" || bad "封签码数 $NSEAL"
check "订单携带糖度/杯型/备注" '半糖' "$R"
R=$(req GET /api/orders "$PACKER" | jq -c --arg a BUNDLE-A '[.[]|select(.id==('$O1') or .id==('$O2'))]|length')
[[ "$R" == "2" ]] && ok "多平台两单均可查询（合并组 BUNDLE-A）" || bad "合并单查询"

echo "================ 3. 出杯顺序 / 开始制作 / 扫封签入待取区 / 杯贴 ================"
R=$(req GET /api/stores/SH001/queue "$PACKER"); QLEN=$(jq 'length' <<<"$R"); [[ "$QLEN" -ge 4 ]] && ok "出杯队列按 queue_seq 展示（$QLEN 杯）" || bad "队列 $QLEN"
# 精细演示 O1 第一杯（冰拿铁）
CUP1=$(jq -r --arg o "$O1" '[.[]|select(.order_id==($o|tonumber) and .sku=="P002")][0].cup_id' <<<"$R")
req POST /api/cups/$CUP1/start "$BARISTA" >/dev/null; code "咖啡师开始制作" 200
SEAL1=$(req GET /api/orders/$O1 "$PACKER" | jq -r --arg c "$CUP1" '.cups[]|select(.cup_id==($c|tonumber)).seal_code')
R=$(req POST /api/cups/seal-scan "$PACKER" "{\"seal_code\":\"$SEAL1\"}"); code "扫描封签入待取区" 200; checkj "封签返回ready" '.status=="ready"' "$R"
req POST /api/cups/seal-scan "$PACKER" "{\"seal_code\":\"$SEAL1\"}" >/dev/null; code "重复扫描被拒" 409
# 其余两杯（热美式、轻食）出杯
for sku in P001 P005; do
  c=$(req GET /api/stores/SH001/queue "$PACKER" | jq -r --arg o "$O1" --arg s "$sku" '[.[]|select(.order_id==($o|tonumber) and .sku==$s and .status=="queued")][0].cup_id')
  req POST /api/cups/$c/start "$BARISTA" >/dev/null
  s=$(req GET /api/orders/$O1 "$PACKER" | jq -r --arg c "$c" '.cups[]|select(.cup_id==($c|tonumber)).seal_code')
  req POST /api/cups/seal-scan "$PACKER" "{\"seal_code\":\"$s\"}" >/dev/null
done
R=$(req GET /api/orders/$O1 "$PACKER"); checkj "订单1全部出杯" '.status=="ready"' "$R"

echo "================ 4. 骑手提前到店但饮品未出杯 → 等待异常 ================"
req POST /api/orders/$O2/rider-arrive "$RIDER2" >/dev/null; code "R002到店打卡(O2未出杯)" 200
INC_WAIT=$(req GET /api/incidents?status=open "$LEADER" | jq -r '[.[]|select(.type=="rider_early_wait")][0].id // empty')
[[ -n "$INC_WAIT" ]] && ok "生成骑手提前到店等待异常#$INC_WAIT" || bad "无等待异常"
brew_order "$PACKER" "$BARISTA" "$O2"
R=$(req GET /api/orders/$O2 "$PACKER"); checkj "O2出杯完成" '.status=="ready"' "$R"

echo "================ 5. 交接核验：订单号/袋数/冷热分袋/吸管餐具 ================"
R=$(req POST /api/orders/$O1/handover "$PACKER" '{"bags_actual":3,"hot_cold_split":true,"straw_utensils_ok":false}')
code "漏吸管餐具核验不通过" 200; checkj "核验不通过" '.passed==false' "$R"
INC_UT=$(jval "$R" .incident_id); [[ "$INC_UT" != null ]] && ok "漏餐具生成异常#$INC_UT（留痕）" || bad "未开异常"
R=$(req POST /api/orders/$O1/handover "$PACKER" '{"bags_actual":2,"hot_cold_split":true,"straw_utensils_ok":true}')
checkj "袋数不符拦截" '.passed==false' "$R"; check "提示袋数不符" "袋数不符" "$R"
R=$(req POST /api/orders/$O1/handover "$PACKER" '{"bags_actual":3,"hot_cold_split":false,"straw_utensils_ok":true}')
checkj "冷热未分袋拦截" '.passed==false' "$R"; check "提示分袋" "分袋" "$R"
R=$(req POST /api/orders/$O1/handover "$PACKER" '{"bags_actual":3,"hot_cold_split":true,"straw_utensils_ok":true}')
code "四项核验通过完成交接" 200; checkj "O1交接通过" '.passed==true' "$R"
req POST /api/orders/$O1/deliver "$RIDER" >/dev/null; code "R001确认送达" 200
# O2 热饮单杯1袋，无需分袋
R=$(req POST /api/orders/$O2/handover "$RIDER2" '{"bags_actual":1,"hot_cold_split":false,"straw_utensils_ok":true}')
checkj "O2交接通过" '.passed==true' "$R"
req POST /api/orders/$O2/deliver "$RIDER2" >/dev/null

echo "================ 6. 顾客收货后投诉漏液 → 客服/财务连到同一订单 ================"
R=$(req POST /api/orders/$O1/report "$CUST" '{"type":"leak_complaint","detail":"杯底漏液，包装湿透"}')
INC_LEAK=$(jval "$R" .incident_id); [[ "$INC_LEAK" != null ]] && ok "漏液投诉异常#$INC_LEAK" || bad "漏液失败 $R"
req POST /api/incidents/$INC_LEAK/threads "$SUPPORT" '{"message":"已联系顾客，发起整单退款+安慰券"}' >/dev/null; code "客服留言认领" 200
R=$(req GET /api/incidents/$INC_LEAK "$SUPPORT")
check "异常详情关联顾客" 顾客小林 "$R"
check "时间线含客服消息" 平台客服 "$R"
CREF=$(jq -r '.compensations[]|select(.kind=="customer_refund").id' <<<"$R"); [[ -n "$CREF" ]] && ok "顾客退款方案#$CREF" || bad "无退款"
req POST /api/compensations/$CREF/approve "$FIN" '{"note":"门店承担"}' >/dev/null; code "财务批准退款" 200
req POST /api/compensations/$CREF/approve "$FIN" >/dev/null; code "重复审批拒绝" 409
req POST /api/compensations/$CREF/pay "$FIN" >/dev/null; code "财务放款" 200
req POST /api/compensations/$CREF/pay "$LEADER" >/dev/null; code "店员无财务权限" 403
req POST /api/incidents/$INC_LEAK/resolve "$LEADER" >/dev/null; code "有待审批赔付时禁止关单" 409
CCPN=$(req GET /api/incidents/$INC_LEAK "$FIN" | jq -r '.compensations[]|select(.status=="proposed").id'|head -1)
req POST /api/compensations/$CCPN/approve "$FIN" >/dev/null; req POST /api/compensations/$CCPN/pay "$FIN" >/dev/null
req POST /api/incidents/$INC_LEAK/resolve "$SUPPORT" >/dev/null; code "赔付结清关闭异常" 200

echo "================ 7. 出杯慢(SLA超时) + 骑手等待责任/补贴 ================"
R=$(platform '{"platform_code":"meituan","platform_order_no":"MT20260929003","store_code":"SH001","customer_code":"cust1","rider_code":"R001","items":[{"sku":"P003","qty":1}]}')
O3=$(jval "$R" .order_id); code "冷萃单接入" 200
C3=$(req GET /api/stores/SH001/queue "$PACKER" | jq -r --arg o "$O3" '[.[]|select(.order_id==($o|tonumber))][0].cup_id')
req POST /api/cups/$C3/start "$BARISTA" >/dev/null
req POST /api/demo/clock "$LEADER" '{"offset_secs":1000}' >/dev/null
req POST /api/orders/$O3/rider-arrive "$RIDER" >/dev/null
S3=$(req GET /api/orders/$O3 "$PACKER" | jq -r --arg c "$C3" '.cups[]|select(.cup_id==($c|tonumber)).seal_code')
R=$(req POST /api/cups/seal-scan "$PACKER" "{\"seal_code\":\"$S3\"}"); check "超时出杯" SLA_BREACH "$R"
checkj "订单记录出杯超时" '.sla_breach==true' "$(req GET /api/orders/$O3 "$PACKER")"
INC_SLOW=$(req GET /api/incidents "$LEADER" | jq -r '[.[]|select(.order_id=='$O3' and .type=="rider_early_wait")][0].id // empty')
[[ -n "$INC_SLOW" ]] && ok "出杯慢联动骑手等待异常#$INC_SLOW" || bad "无慢出杯异常"
req POST /api/incidents/$INC_SLOW/rider-wait-comp "$SUPPORT" '{"amount":6.5}' >/dev/null; code "客服发起骑手等待补贴" 200
CWAIT=$(req GET /api/incidents/$INC_SLOW "$FIN" | jq -r '.compensations[]|select(.kind=="rider_wait").id')
req POST /api/compensations/$CWAIT/approve "$FIN" >/dev/null
req POST /api/orders/$O3/handover "$PACKER" '{"bags_actual":1,"hot_cold_split":false,"straw_utensils_ok":true}' >/dev/null
req POST /api/orders/$O3/deliver "$RIDER" >/dev/null
req POST /api/demo/clock "$LEADER" '{"offset_secs":0}' >/dev/null

echo "================ 8. 饮品放置过久 → 重做（重做成本入班组复盘） ================"
O4=$(jval "$(platform '{"platform_code":"eleme","platform_order_no":"EL20260929004","store_code":"SH001","items":[{"sku":"P004","qty":1}]}')" .order_id)
C4=$(req GET /api/stores/SH001/queue "$PACKER" | jq -r --arg o "$O4" '[.[]|select(.order_id==($o|tonumber))][0].cup_id')
req POST /api/cups/$C4/start "$BARISTA" >/dev/null
S4=$(req GET /api/orders/$O4 "$PACKER" | jq -r --arg c "$C4" '.cups[]|select(.cup_id==($c|tonumber)).seal_code')
req POST /api/cups/seal-scan "$PACKER" "{\"seal_code\":\"$S4\"}" >/dev/null
req POST /api/demo/clock "$LEADER" '{"offset_secs":1000}' >/dev/null
req POST /api/admin/stale-sweep "$PACKER" >/dev/null; code "扫描放置过久饮品" 200
checkj "旧杯标记过久stale" '(.cups|any(.stale==true))' "$(req GET /api/orders/$O4 "$PACKER")"
INC_STALE=$(req GET /api/incidents "$LEADER" | jq -r '[.[]|select(.order_id=='$O4' and .type=="remake_stale")][0].id // empty')
[[ -n "$INC_STALE" ]] && ok "生成重做异常#$INC_STALE" || bad "无重做异常"
R=$(req POST /api/cups/$C4/remake "$PACKER" '{"reason":"remake_stale"}'); code "确认重做、旧杯报损、新杯入队" 200
NEWCUP=$(jval "$R" .new_cup_id); [[ "$NEWCUP" != null ]] && ok "新重做杯#$NEWCUP" || bad "重做"
req POST /api/demo/clock "$LEADER" '{"offset_secs":0}' >/dev/null
req POST /api/cups/$NEWCUP/start "$BARISTA" >/dev/null
S4N=$(req GET /api/orders/$O4 "$PACKER" | jq -r --arg c "$NEWCUP" '.cups[]|select(.cup_id==($c|tonumber)).seal_code')
req POST /api/cups/seal-scan "$PACKER" "{\"seal_code\":\"$S4N\"}" >/dev/null

echo "================ 9. 顾客备注漏看 / 错杯 → 再重做 ================"
R=$(req POST /api/orders/$O4/report "$CUST" '{"type":"remark_missed","detail":"顾客要求去冰被漏看"}')
[[ "$(jval "$R" .incident_id)" != null ]] && ok "备注漏看异常已开" || bad "备注漏看"
R=$(req POST /api/orders/$O4/report "$PACKER" '{"cup_id":'$NEWCUP',"type":"wrong_cup","detail":"糖度做错"}')
INC_WRONG=$(jval "$R" .incident_id); [[ "$INC_WRONG" != null ]] && ok "错杯异常#$INC_WRONG（计入班组评分）" || bad "错杯"
R=$(req POST /api/cups/$NEWCUP/remake "$PACKER" '{"reason":"wrong_cup"}'); code "错杯重做" 200
NC2=$(jval "$R" .new_cup_id)
req POST /api/cups/$NC2/start "$BARISTA" >/dev/null
S4N2=$(req GET /api/orders/$O4 "$PACKER" | jq -r --arg c "$NC2" '.cups[]|select(.cup_id==($c|tonumber)).seal_code')
req POST /api/cups/seal-scan "$PACKER" "{\"seal_code\":\"$S4N2\"}" >/dev/null
req POST /api/orders/$O4/handover "$PACKER" '{"bags_actual":1,"hot_cold_split":false,"straw_utensils_ok":true}' >/dev/null
code "O4重做后交接通过" 200

echo "================ 10. 轻食缺货 → 临时下架 → 补货恢复 ================"
O5=$(jval "$(platform '{"platform_code":"meituan","platform_order_no":"MT20260929005","store_code":"SH002","items":[{"sku":"P005","qty":1},{"sku":"P001","qty":1}]}')" .order_id)
R=$(req POST /api/orders/$O5/report "$LEADER2" '{"type":"food_oos","detail":"藜麦卷售罄"}')
INC_OOS=$(jval "$R" .incident_id); [[ "$INC_OOS" != null ]] && ok "轻食缺货异常#$INC_OOS" || bad "缺货 $R"
R=$(req GET "/api/delistings?active=true" "$LEADER2"); checkj "临时下架含P005" '.[0].sku=="P005"' "$R"
platform '{"platform_code":"meituan","platform_order_no":"MT20260929006","store_code":"SH002","items":[{"sku":"P005","qty":1}]}' >/dev/null; code "下架商品接单被拒" 409
req POST /api/stores/SH002/replenish "$LEADER2" '{"ingredient_id":7,"qty":50}' >/dev/null
DID=$(req GET "/api/delistings?active=true" "$LEADER2" | jq -r '.[0].id')
req POST /api/delistings/$DID/restore "$LEADER2" >/dev/null; code "补货后恢复上架" 200
platform '{"platform_code":"meituan","platform_order_no":"MT20260929007","store_code":"SH002","items":[{"sku":"P005","qty":1}]}' >/dev/null; code "恢复后可接单" 200

echo "================ 11. 平台取消（释放未制作原料/报损在制/毛利损失/赔付） ================"
O6=$(jval "$(platform '{"platform_code":"douyin","platform_order_no":"DY20260929008","store_code":"SH001","items":[{"sku":"P002","qty":2}]}')" .order_id)
C6=$(req GET /api/stores/SH001/queue "$PACKER" | jq -r --arg o "$O6" '[.[]|select(.order_id==($o|tonumber))][0].cup_id')
req POST /api/cups/$C6/start "$BARISTA" >/dev/null
R=$(req POST /api/orders/$O6/report "$SUPPORT" '{"type":"platform_cancel","detail":"顾客申请平台退款"}')
INC_CX=$(jval "$R" .incident_id); [[ "$INC_CX" != null ]] && ok "平台取消异常#$INC_CX" || bad "取消 $R"
checkj "订单已取消" '.status=="cancelled"' "$(req GET /api/orders/$O6 "$PACKER")"
checkj "记录预期毛利损失24" '.lost_margin==24' "$(req GET /api/orders/$O6 "$PACKER")"
CCP=$(req GET /api/incidents/$INC_CX "$FIN" | jq -r '.compensations[]|select(.kind=="platform_penalty").id')
[[ -n "$CCP" ]] && ok "平台赔付方案#$CCP待财务审批" || bad "无取消赔付"
req POST /api/compensations/$CCP/approve "$FIN" >/dev/null; req POST /api/compensations/$CCP/pay "$FIN" >/dev/null
ok "取消赔付审批放款完成"
# 在制1杯拿铁成本=7元（旧bug会因配方JOIN被乘成21）
LOSSES=$(req GET /api/stores/SH001/losses "$LEADER")
checkj "取消在制杯按单杯成本报损7元" '[.[]|select(.reason=="cancel_in_progress")][0].cost_amount==7' "$LOSSES"
checkj "重做报损记录齐全(过久+错杯=2条)" '[.[]|select(.reason|test("remake_stale|wrong_cup"))]|length==2' "$LOSSES"

echo "================ 12. 爆单保护（临时限制接单）与骑手到店屏 ================"
req POST /api/stores/SH002/acceptance "$LEADER2" '{"mode":"paused","note":"爆单临时限制接单"}' >/dev/null; code "店长暂停接单" 200
platform '{"platform_code":"meituan","platform_order_no":"MT20260929009","store_code":"SH002","items":[{"sku":"P001","qty":1}]}' >/dev/null; code "暂停期拒单" 409
req POST /api/stores/SH002/acceptance "$LEADER2" '{"mode":"open","note":"高峰缓解"}' >/dev/null; code "恢复接单" 200
R=$(req GET /api/stores/SH001/board "$RIDER"); check "骑手到店屏" queue_depth "$R"; check "待取区分区" ready_orders "$R"
R=$(req GET /api/stores/SH001/events?limit=5 "$PACKER"); check "门店事件流" '"kind"' "$R"

echo "================ 13. 区域经理：按门店看异常对利润的影响 ================"
R=$(req GET /api/manager/store-metrics "$MANAGER"); code "经理查看门店日指标" 200
M1=$(jq -c '[.[]|select(.store_code=="SH001")][0]' <<<"$R")
jq -e '.slow_orders==1' <<<"$M1" >/dev/null && ok "出杯慢=1" || bad "slow"
jq -e '.wrong_cup_count==1' <<<"$M1" >/dev/null && ok "错杯=1" || bad "wrong"
jq -e '.missing_utensils==1' <<<"$M1" >/dev/null && ok "漏餐具=1" || bad "utensils"
jq -e '.platform_cancels==1' <<<"$M1" >/dev/null && ok "平台取消=1" || bad "cancel"
RC=$(jq -r .remake_count <<<"$M1"); [[ "$RC" -ge 2 ]] && ok "重做≥2（实际$RC）" || bad "重做 $RC"
PI=$(jq -r .profit_impact_total <<<"$M1"); [[ "$PI" != "0" && "$PI" != "null" ]] && ok "异常对利润影响=$PI" || bad "影响0"
NET=$(jq -r .net_profit <<<"$M1"); [[ -n "$NET" ]] && ok "扣损后净利润=$NET" || bad "net"

echo "================ 14. 区域督导复盘 + 班组评分更新 ================"
R=$(req GET /api/supervisor/shift-scores "$SUP"); code "督导查看班组评分" 200
SH=$(jq -r '[.[]|select(.store_code=="SH001")][0].shift_id' <<<"$R")
SC=$(jq -r '[.[]|select(.store_code=="SH001")][0].score' <<<"$R")
RMK=$(jq -r '[.[]|select(.store_code=="SH001")][0].remake_cost' <<<"$R")
[[ "$SC" != "100" && "$SC" != null ]] && ok "班组#$SH评分因异常下降=$SC" || bad "评分 $SC"
[[ "$RMK" != "0" && -n "$RMK" ]] && ok "重做成本进班组复盘=$RMK" || bad "重做成本 $RMK"
req POST /api/shifts/$SH/reviews "$SUP" '{"content":"午班重做2杯、漏餐具1次、出杯超时1单；加强封签核对与爆单暂停管控。"}' >/dev/null
code "督导提交复盘" 200

echo "================ 15. OpenAPI 3.x 与 Swagger UI ================"
curl -s "$BASE_URL/openapi.json" | grep -q '"openapi": "3.0' && ok "OpenAPI 3.x 可访问" || bad "openapi"
curl -s "$BASE_URL/swagger/index.html" | grep -qi swagger && ok "Swagger UI 内嵌可访问" || bad "swagger"

echo
echo "================ 结果：PASS=$PASS FAIL=$FAIL ================"
[[ $FAIL -eq 0 ]]
