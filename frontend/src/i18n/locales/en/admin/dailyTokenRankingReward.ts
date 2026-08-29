export default {
  dailyTokenRankingReward: {
      title: 'Daily Token Ranking Rewards',
      description: 'Review the previous day\'s Beijing-time ranking and issue rewards to the top three users.',
      date: 'Settlement date',
      timezone: 'Timezone',
      settled: 'Settled',
      pending: 'Pending',
      candidates: 'Reward candidates',
      rewardRule: '1st $3 · 2nd $2 · 3rd $1',
      rank: 'Rank',
      user: 'User',
      tokens: 'Tokens',
      requests: 'Requests',
      reward: 'Reward',
      status: 'Status',
      note: 'Note',
      settle: 'Issue rewards',
      loadMock: 'Load test data',
      mockMode: 'Test data',
      confirmSettle: 'Issue the ranking rewards for this date? This cannot be settled twice.',
      confirmMockSettle: 'This is test ranking data, but it will add real balance and create settlement records for these users. Continue?',
      settleSuccess: 'Ranking rewards issued',
      loadFailed: 'Failed to load settlement data',
      settleFailed: 'Failed to issue ranking rewards',
      empty: 'No eligible ranking users for this date.',
      statuses: { pending: 'Pending', paid: 'Paid', skipped: 'Skipped', empty: 'No eligible users' }
  }
}
