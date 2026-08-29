export default {
  admin: {
    dailyTokenRankingReward: {
      title: '每日 Token 排行奖励结算',
      description: '按北京时区核对上一日排行并发放前三名余额奖励。',
      date: '结算日期',
      timezone: '统计时区',
      settled: '已结算',
      pending: '待结算',
      candidates: '奖励候选人',
      rewardRule: '第一名 $3 · 第二名 $2 · 第三名 $1',
      rank: '排名',
      user: '用户',
      tokens: 'Token 数量',
      requests: '调用次数',
      reward: '奖励金额',
      status: '状态',
      note: '备注',
      settle: '确认发放',
      confirmSettle: '确认发放该日期的排行奖励？发放后不可重复结算。',
      settleSuccess: '排行奖励已发放',
      loadFailed: '加载结算数据失败',
      settleFailed: '发放排行奖励失败',
      empty: '该日期没有符合条件的排行用户。',
      statuses: { pending: '待发放', paid: '已发放', skipped: '已跳过', empty: '无符合条件用户' }
    }
  }
}
