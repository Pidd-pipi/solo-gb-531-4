export const rectificationStates = ['pending', 'in_progress', 'pending_review', 'completed', 'voided'] as const
export type RectificationState = (typeof rectificationStates)[number]

export const rectificationStateLabels: Record<RectificationState, string> = {
  pending: '待处理',
  in_progress: '整改中',
  pending_review: '待复核',
  completed: '已完成',
  voided: '已作废',
}
