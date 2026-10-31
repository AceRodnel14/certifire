// Open/close rules of the info tooltip. Hover (mouse pointers only), focus and click/tap only
// ever open it, so a tap never toggles it closed; it closes on leave, blur, Escape or an
// outside pointer press.
export type TipEvent = 'enter' | 'leave' | 'focus' | 'blur' | 'click' | 'escape' | 'outside'

export const tipOpen = (ev: TipEvent): boolean => ev === 'enter' || ev === 'focus' || ev === 'click'
