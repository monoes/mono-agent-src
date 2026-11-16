import { validateChoices } from './choicesSchema.js'
import { ChoicesBlock } from './ChoicesBlock.jsx'
import { validateChart } from './chartSchema.js'
import { ChartBlock } from './ChartBlock.jsx'
import { validateTable } from './tableSchema.js'
import { TableBlock } from './TableBlock.jsx'
import { validateCard, validateList } from './cardSchema.js'
import { CardBlock, ListBlock } from './CardBlock.jsx'
import { validateSteps, StepsBlock } from './StepsBlock.jsx'
import { validateForm } from './formSchema.js'
import { FormBlock } from './FormBlock.jsx'

// One entry per block type: { validate(json) -> data (throws Bad), Component }.
// Each block module adds itself here.
export const BLOCKS = {
  choices: { validate: validateChoices, Component: ChoicesBlock },
  chart: { validate: validateChart, Component: ChartBlock },
  table: { validate: validateTable, Component: TableBlock },
  card: { validate: validateCard, Component: CardBlock },
  list: { validate: validateList, Component: ListBlock },
  steps: { validate: validateSteps, Component: StepsBlock },
  form: { validate: validateForm, Component: FormBlock },
}
