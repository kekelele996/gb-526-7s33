import { create } from 'zustand'
import { approveAssessment, compareAssessments, getAssessment, listAssessments, returnAssessment, runAssessment, submitAssessment } from '@/api/assessment'
import { errorMessage } from '@/api/client'
import type { AssessmentComparison, DecompressionAssessment } from '@/types/assessment'

interface AssessmentStore {
  items: DecompressionAssessment[]
  selected: DecompressionAssessment | null
  comparison: AssessmentComparison | null
  loading: boolean
  error: string | null
  load: (planId?: number) => Promise<void>
  select: (id: number) => Promise<void>
  run: (planId: number, version: number) => Promise<DecompressionAssessment>
  submit: (id: number, version: number, reason: string) => Promise<DecompressionAssessment>
  approve: (id: number, version: number, reason: string) => Promise<DecompressionAssessment>
  returnForRework: (id: number, version: number, reason: string) => Promise<DecompressionAssessment>
  compare: (leftId: number, rightId: number) => Promise<void>
}

const byRevision = (a: DecompressionAssessment, b: DecompressionAssessment) =>
  b.revision - a.revision || b.id - a.id

const patch = (items: DecompressionAssessment[], item: DecompressionAssessment) =>
  items.map((current) => (current.id === item.id ? item : current))

export const useAssessmentStore = create<AssessmentStore>((set, get) => ({
  items: [], selected: null, comparison: null, loading: false, error: null,
  load: async (planId) => {
    set({ loading: true, error: null })
    try {
      const page = await listAssessments(planId)
      const items = page.items.sort(byRevision)
      const selectedId = get().selected?.id ?? items[0]?.id
      set({ items, loading: false })
      if (selectedId && items.some((item) => item.id === selectedId)) await get().select(selectedId)
      else set({ selected: items[0] ?? null })
    } catch (error) { set({ error: errorMessage(error), loading: false }) }
  },
  select: async (id) => {
    try { set({ selected: await getAssessment(id), error: null, comparison: null }) }
    catch (error) { set({ error: errorMessage(error) }) }
  },
  run: async (planId, version) => {
    const item = await runAssessment(planId, version)
    set((state) => ({ items: [item, ...state.items].sort(byRevision), selected: item }))
    return item
  },
  submit: async (id, version, reason) => {
    const item = await submitAssessment(id, version, reason)
    set((state) => ({ items: patch(state.items, item), selected: item }))
    return item
  },
  approve: async (id, version, reason) => {
    const item = await approveAssessment(id, version, reason)
    set((state) => ({ items: patch(state.items, item), selected: item }))
    return item
  },
  returnForRework: async (id, version, reason) => {
    const item = await returnAssessment(id, version, reason)
    // The returned revision stays selected so the reason and the next action
    // stay visible; the queue keeps it ahead of older revisions.
    set((state) => ({ items: patch(state.items, item).sort(byRevision), selected: item }))
    return item
  },
  compare: async (leftId, rightId) => {
    try { set({ comparison: await compareAssessments(leftId, rightId), error: null }) }
    catch (error) { set({ error: errorMessage(error) }) }
  },
}))
