-- 种子数据（幂等，可重复执行）

INSERT INTO regions (id, name) VALUES (1, '华东大区')
ON CONFLICT (id) DO NOTHING;
SELECT setval(pg_get_serial_sequence('regions','id'), GREATEST((SELECT MAX(id) FROM regions),1));

INSERT INTO stores (id, region_id, code, name, address, hourly_capacity, overload_queue_threshold, stale_after_secs, handover_sla_secs)
VALUES
 (1, 1, 'SH001', '南京西路店', '上海市静安区南京西路888号', 80, 12, 900, 900),
 (2, 1, 'SH002', '陆家嘴店',   '上海市浦东新区世纪大道100号', 60, 10, 900, 900)
ON CONFLICT (id) DO NOTHING;
SELECT setval(pg_get_serial_sequence('stores','id'), GREATEST((SELECT MAX(id) FROM stores),1));

INSERT INTO platforms (id, code, name) VALUES
 (1,'meituan','美团外卖'),
 (2,'eleme','饿了么'),
 (3,'douyin','抖音外卖')
ON CONFLICT (id) DO NOTHING;
SELECT setval(pg_get_serial_sequence('platforms','id'), GREATEST((SELECT MAX(id) FROM platforms),1));

-- 演示账号密码由服务启动时的 seeder 统一写入 bcrypt（admin=admin123，其余=demo123）
INSERT INTO users (id, username, password, display_name, role, staff_title, region_id, store_id, platform_id) VALUES
 (1,'admin',       'RESET_ME','平台管理员','admin',            NULL,    NULL, NULL, NULL),
 (2,'manager1',    'RESET_ME','陈经理','area_manager',         NULL,    1,    NULL, NULL),
 (3,'supervisor1', 'RESET_ME','周督导','supervisor',          NULL,    1,    NULL, NULL),
 (4,'leader1',     'RESET_ME','赵店长','store_staff',         '店长',  NULL, 1,    NULL),
 (5,'barista1',    'RESET_ME','孙咖啡师','store_staff',       '咖啡师',NULL, 1,    NULL),
 (6,'packer1',     'RESET_ME','钱打包员','store_staff',       '打包员',NULL, 1,    NULL),
 (7,'leader2',     'RESET_ME','吴店长','store_staff',         '店长',  NULL, 2,    NULL),
 (8,'support1',    'RESET_ME','美团客服小M','platform_support',NULL,  NULL, NULL, 1),
 (9,'finance1',    'RESET_ME','财务小付','finance',           NULL,    NULL, NULL, NULL)
ON CONFLICT (id) DO NOTHING;
SELECT setval(pg_get_serial_sequence('users','id'), GREATEST((SELECT MAX(id) FROM users),1));

INSERT INTO riders (id, rider_code, name, phone, platform_id) VALUES
 (1,'R001','王骁','13800000001',1),
 (2,'R002','李奔','13800000002',2)
ON CONFLICT (id) DO NOTHING;
SELECT setval(pg_get_serial_sequence('riders','id'), GREATEST((SELECT MAX(id) FROM riders),1));

INSERT INTO customers (id, access_code, name, phone) VALUES
 (1,'cust1','顾客小林','13900000001'),
 (2,'cust2','顾客小周','13900000002')
ON CONFLICT (id) DO NOTHING;
SELECT setval(pg_get_serial_sequence('customers','id'), GREATEST((SELECT MAX(id) FROM customers),1));

INSERT INTO ingredients (id, name, unit) VALUES
 (1,'意式浓缩液','ml'),
 (2,'牛奶','ml'),
 (3,'咖啡豆','g'),
 (4,'冷萃液','ml'),
 (5,'果糖','ml'),
 (6,'水果风味酱','g'),
 (7,'轻食原料','份'),
 (8,'杯盖杯身','个'),
 (9,'吸管','根'),
 (10,'餐具包','套')
ON CONFLICT (id) DO NOTHING;
SELECT setval(pg_get_serial_sequence('ingredients','id'), GREATEST((SELECT MAX(id) FROM ingredients),1));

-- category: americano 美式 / latte 拿铁 / cold_brew 冷萃 / fruit_coffee 果咖 /
--           light_food 轻食 / combo 组合套餐
INSERT INTO products (id, sku, name, category, price, cost, make_secs, needs_straw, needs_utensils, bag_class) VALUES
 (1,'P001','美式咖啡','americano',      15.00, 5.00,  90, true,  false,'cold'),
 (2,'P002','丝绒拿铁','latte',          19.00, 7.00, 120, true,  false,'cold'),
 (3,'P003','冰滴冷萃','cold_brew',      22.00, 8.00, 150, true,  false,'cold'),
 (4,'P004','橙C果咖','fruit_coffee',    21.00, 8.00, 120, true,  false,'cold'),
 (5,'P005','鸡肉藜麦卷','light_food',   25.00,12.00,  60, false, true, 'food'),
 (6,'P006','美式+藜麦卷套餐','combo',   36.00,16.00, 180, true,  true, 'cold')
ON CONFLICT (id) DO NOTHING;
SELECT setval(pg_get_serial_sequence('products','id'), GREATEST((SELECT MAX(id) FROM products),1));

INSERT INTO recipes (product_id, ingredient_id, qty) VALUES
 (1,3,18),(1,8,1),
 (2,1,60),(2,2,180),(2,8,1),
 (3,4,240),(3,8,1),
 (4,1,60),(4,6,40),(4,5,10),(4,8,1),
 (5,7,1),(5,10,1),
 (6,3,18),(6,7,1),(6,8,1),(6,10,1)
ON CONFLICT DO NOTHING;

-- 门店原料库存（SH002 轻食原料仅 2 份，用于演示缺货临时下架）
INSERT INTO inventory (store_id, ingredient_id, qty) VALUES
 (1,1,10000),(1,2,20000),(1,3,5000),(1,4,8000),(1,5,5000),(1,6,3000),(1,7,200),(1,8,5000),(1,9,5000),(1,10,2000),
 (2,1,8000),(2,2,15000),(2,3,4000),(2,4,6000),(2,5,4000),(2,6,2000),(2,7,2),(2,8,4000),(2,9,4000),(2,10,1500)
ON CONFLICT DO NOTHING;
