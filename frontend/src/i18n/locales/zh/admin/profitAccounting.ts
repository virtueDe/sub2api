export default {
  profitAccounting: {
    unconfirmedCharge: '零扣费记录，需确认是否成功计费', sourceIncomplete: '源数据保留范围不足，无法确认该日账单完整性。', title: '利润核算', description: '余额生图业务的收入、采购成本与毛利',
    schedule: '美元计价 · 北京时间每日 07:00 核算昨日 · 失败不收费',
    reports: '利润日报', prices: '采购单价', addPrice: '新增价格版本', recalculate: '生成／重算日报',
    startDate: '开始日期', endDate: '结束日期', refresh: '查询', date: '账单日期',
    revenue: '实际扣费收入', cost: '已核算成本', profit: '毛利', images: '计费张数', requests: '调用数',
    status: '状态', complete: '已完成', incomplete: '待补全成本', pending: '待核算',
    missing: '待核对调用', actions: '操作', detail: '查看明细', revisions: '历史版本', revision: '版本',
    account: '账号', channel: '渠道', model: '实际上游模型', resolution: '计费分辨率',
    unitPrice: '采购价（美元／张）', effectiveAt: '生效时间（北京时间）', note: '配置原因',
    scope: '价格适用范围', chooseScope: '搜索并选择账号或渠道', save: '保存新版本', cancel: '取消',
    priceHelp: '账号价格优先于渠道默认价。调价只影响生效后的调用；历史更正需填写过去的生效时间，再主动重算对应日期。',
    emptyReports: '暂无日报。系统会自动补算，也可选择已结束的日期手动生成。',
    emptyPrices: '请先配置采购单价。未匹配的记录会保留收入，但不会显示完整毛利。',
    reason: '重算原因', reasonHelp: '重算会新增版本并保留旧日报，不会修改用户扣费。',
    detailHelp: '按调用记录入账日核算；同一调用的收入只计一次，采购成本按各分辨率及当时价格分别计算。',
    unknown: '未记录', missingPrice: '缺少采购单价', unknownResolution: '缺少分辨率',
    unknownCount: '缺少计费张数', inconsistentCount: '图片数量与分辨率明细不一致',
    fillPrice: '补充采购价', changePrice: '调整价格', priceVersion: '价格版本',
    saved: '采购价格版本已保存；历史日报需主动重算', calculated: '日报已生成', failed: '操作失败，请重试',
    required: '请填写完整信息，采购价必须为非负数', zeroPrice: '0 表示免费采购；未知成本请不要填 0。',
    scheduled: '自动结算', createdAt: '核算时间', historyNote: '选择版本查看原始核算结果。',
    pendingHelp: '已核算成本仅包含匹配成功的部分；待核对记录补全前，毛利不显示。',
    immediate: '留空表示立即生效', noChannel: '无渠道快照'
  }
}
