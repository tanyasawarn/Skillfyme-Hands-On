/**
 * Loads the GenAI-with-ML skill graph derived from Skillfyme's
 * "Generative AI With ML Masters Program" curriculum (7 modules: Python &
 * Stats, Applied ML, Deep Learning, NLP, GenAI Essentials & Prompt
 * Engineering, LLM Application Development, Agentic AI & LLMOps).
 * Second independent course, same authoring rigor as
 * seed-skills.ts (DevOps With AI): curriculum PDF is the source of truth
 * for skill names/grouping, edges follow the actual teaching sequence
 * (session order within a module, module order across the course), no
 * invented skills outside curriculum scope.
 *
 * course_slug: 'genai-with-ml' scopes every row to this course, keeping
 * it independent from the DevOps track's skill graph even where domain
 * names would otherwise collide (e.g. both courses have a "core" domain).
 *
 * Run with: npx ts-node -r tsconfig-paths/register scripts/seed-skills-genai.ts
 */
import { Kysely, PostgresDialect } from 'kysely';
import { Pool } from 'pg';
import type { Database } from '../src/db/schema';
import { SkillRepository } from '../src/modules/skill/skill.repository';

const COURSE_SLUG = 'genai-with-ml';

interface SkillDef {
  slug: string;
  name: string;
  domain: string;
  description: string;
}

interface EdgeDef {
  from: string;
  to: string;
}

// --- Module 1: Python & Statistics Essentials (curriculum pg.5-8) --------
const PYTHON_STATS: SkillDef[] = [
  { slug: 'python.fundamentals', name: 'Python Programming Fundamentals', domain: 'python-stats', description: 'Syntax, data types, operators, and interactive coding basics.' },
  { slug: 'python.data-structures-control-flow', name: 'Data Structures, Control Flow & File Operations', domain: 'python-stats', description: 'Lists, tuples, sets, dicts, conditionals, loops, and file I/O.' },
  { slug: 'python.functions-oop-exceptions', name: 'Functions, OOP & Exception Handling', domain: 'python-stats', description: 'Reusable functions, classes/objects, inheritance, and error handling.' },
  { slug: 'python.data-analysis-visualization', name: 'Data Analysis & Visualization', domain: 'python-stats', description: 'Pandas, NumPy, Matplotlib/Seaborn for EDA and plotting.' },
];

// --- Module 2: Applied Machine Learning (curriculum pg.9-14) --------------
const APPLIED_ML: SkillDef[] = [
  { slug: 'ml.data-preprocessing-feature-engineering', name: 'Data Pre-processing & Feature Engineering', domain: 'applied-ml', description: 'Missing data, outliers, scaling, encoding, and feature creation/selection.' },
  { slug: 'ml.supervised-regression', name: 'Supervised Learning — Regression', domain: 'applied-ml', description: 'Linear/regularized regression, polynomial terms, and tree-based regressors.' },
  { slug: 'ml.supervised-classification', name: 'Supervised Learning — Classification', domain: 'applied-ml', description: 'Logistic regression, KNN, decision trees, random forests, boosting, evaluation metrics.' },
  { slug: 'ml.unsupervised-learning', name: 'Unsupervised Learning', domain: 'applied-ml', description: 'K-Means, hierarchical and density-based clustering, dimensionality reduction intro.' },
  { slug: 'ml.clustering-dimensionality-reduction', name: 'Clustering Algorithms & Dimensionality Reduction', domain: 'applied-ml', description: 'PCA, t-SNE, UMAP, Gaussian Mixture Models for high-dimensional data.' },
  { slug: 'ml.ensemble-learning', name: 'Ensemble Learning', domain: 'applied-ml', description: 'Bagging, boosting, stacking; Random Forest, XGBoost, LightGBM, CatBoost.' },
];

// --- Module 3: Deep Learning & Neural Network Architectures (pg.15-19) ---
const DEEP_LEARNING: SkillDef[] = [
  { slug: 'dl.neural-network-foundations', name: 'Neural Networks & Deep Learning Foundations', domain: 'deep-learning', description: 'Perceptrons, forward propagation, activations, loss functions, backprop, optimizers.' },
  { slug: 'dl.tuning-optimizing-networks', name: 'Tuning & Optimizing Deep Neural Networks', domain: 'deep-learning', description: 'Hyperparameter tuning, weight init, regularization, batch norm, LR schedules.' },
  { slug: 'dl.cnn-fundamentals', name: 'Convolutional Neural Networks — I', domain: 'deep-learning', description: 'Convolution, pooling, CNN architecture, receptive fields, LeNet/AlexNet.' },
  { slug: 'dl.cnn-advanced', name: 'Convolutional Neural Networks — II', domain: 'deep-learning', description: 'VGG, ResNet, DenseNet, transfer learning, object detection intro.' },
  { slug: 'dl.recurrent-networks', name: 'Recurrent Neural Networks', domain: 'deep-learning', description: 'Sequence modeling fundamentals and RNN architecture.' },
  { slug: 'dl.lstm-networks', name: 'Long Short-Term Memory (LSTM) Networks', domain: 'deep-learning', description: 'LSTM gates, cell state, GRU trade-offs, seq2seq, attention introduction.' },
];

// --- Module 4: Natural Language Processing (curriculum pg.20-28) ---------
const NLP: SkillDef[] = [
  { slug: 'nlp.introduction', name: 'Introduction to NLP', domain: 'nlp', description: 'NLP evolution, applications, Bag-of-Words/TF-IDF, evaluation metrics.' },
  { slug: 'nlp.text-processing-feature-engineering', name: 'Text Processing & Feature Engineering', domain: 'nlp', description: 'Cleaning, stemming/lemmatization, n-grams, term weighting, OOV handling.' },
  { slug: 'nlp.ner-parsing', name: 'Named Entity Recognition (NER) & Parsing', domain: 'nlp', description: 'Entity extraction, POS tagging, dependency/constituency parsing.' },
  { slug: 'nlp.tokenization-encoding', name: 'Tokenization & Text Encoding', domain: 'nlp', description: 'Word/subword/character tokenization, Word2Vec/GloVe, contextual embeddings.' },
  { slug: 'nlp.sentiment-analysis-essentials', name: 'Sentiment Analysis Essentials', domain: 'nlp', description: 'Lexicon-based and traditional ML sentiment classifiers.' },
  { slug: 'nlp.sentiment-analysis-advanced', name: 'Advanced Sentiment Analysis', domain: 'nlp', description: 'RNN/transformer-based sentiment, fine-tuning BERT, aspect-based analysis.' },
  { slug: 'nlp.neural-language-models', name: 'Neural Language Models', domain: 'nlp', description: 'Statistical vs neural LMs, seq2seq, attention/Transformer, scaling laws.' },
  { slug: 'nlp.machine-translation', name: 'Machine Translation', domain: 'nlp', description: 'Encoder-decoder MT, attention, BLEU evaluation, Transformer-based MT.' },
  { slug: 'nlp.speech-multimodal', name: 'Speech & Multimodal NLP', domain: 'nlp', description: 'ASR pipelines, audio features, multimodal embeddings, vision+language tasks.' },
];

// --- Module 5: GenAI Essentials & Prompt Engineering (pg.29-34) -----------
const GENAI_ESSENTIALS: SkillDef[] = [
  { slug: 'genai.introduction', name: 'Introduction to Generative AI', domain: 'genai-essentials', description: 'Generative vs discriminative models, history (RBM->VAE->GAN->Transformer), evaluation metrics.' },
  { slug: 'genai.autoencoders-gans', name: 'Autoencoders & GANs', domain: 'genai-essentials', description: 'Autoencoder/VAE architecture, GAN generator-discriminator, DCGAN/CycleGAN/StyleGAN.' },
  { slug: 'genai.transformers-attention', name: 'Transformers & Attention Mechanism', domain: 'genai-essentials', description: 'Self-attention, scaled dot-product attention, multi-head attention, encoder/decoder.' },
  { slug: 'genai.small-language-models', name: 'Small Language Models', domain: 'genai-essentials', description: 'Distillation, quantization/pruning, SLM vs LLM trade-offs.' },
  { slug: 'genai.prompt-engineering-essentials', name: 'Prompt Engineering Essentials', domain: 'genai-essentials', description: 'Zero-shot, few-shot, chain-of-thought, instruction/role prompting.' },
  { slug: 'genai.advanced-prompting', name: 'Advanced Prompting Strategies', domain: 'genai-essentials', description: 'Self-consistency, prompt chaining, retrieval-augmented prompting, guardrails.' },
];

// --- Module 6: LLM-based Application Development (pg.35-44) ---------------
const LLM_APPS: SkillDef[] = [
  { slug: 'llm.introduction', name: 'Introduction to Large Language Models', domain: 'llm-apps', description: 'LLM evolution, transformer deep dive, scaling laws, pretraining objectives.' },
  { slug: 'llm.open-source-model-variants', name: 'Open Source LLMs & Model Variants', domain: 'llm-apps', description: 'LLaMA 2, Falcon, Mistral, BLOOM; quantization/pruning for inference.' },
  { slug: 'llm.vector-databases', name: 'Vector Databases', domain: 'llm-apps', description: 'Text embeddings, similarity search, FAISS/Pinecone/Weaviate/Chroma, indexing strategies.' },
  { slug: 'llm.rag-techniques', name: 'Retrieval-Augmented Generation (RAG) Techniques', domain: 'llm-apps', description: 'Retriever+reader architecture, chunking, query rewriting/reranking, hybrid retrieval.' },
  { slug: 'llm.frameworks-development', name: 'LLM Frameworks & Development', domain: 'llm-apps', description: 'LangChain, LlamaIndex, Hugging Face + PEFT, agent architectures (ReAct).' },
  { slug: 'llm.application-development', name: 'LLM Application Development', domain: 'llm-apps', description: 'Prompt patterns, tool usage, conversation memory, context window optimization.' },
  { slug: 'llm.fine-tuning-adaptation', name: 'Fine-tuning & Model Adaptation', domain: 'llm-apps', description: 'Full fine-tuning vs LoRA/QLoRA/adapters, RLHF alignment, dataset curation.' },
  { slug: 'llm.application-deployment', name: 'Generative AI Application Deployment', domain: 'llm-apps', description: 'REST/gRPC/serverless deployment, GPU scheduling, inference load balancing, LLMOps.' },
  { slug: 'llm.caching-routing', name: 'LLM Caching & Routing Techniques', domain: 'llm-apps', description: 'Prompt/semantic caching, KV cache reuse, multi-LLM routing, Mixture-of-Experts.' },
  { slug: 'llm.evaluation-responsible-ai', name: 'Evaluation, Metrics & Responsible AI for LLMs', domain: 'llm-apps', description: 'BLEU/ROUGE/BERTScore, human eval, bias/toxicity mitigation, governance/compliance.' },
];

// --- Module 7: Agentic AI & LLMOps for Scalable AI Systems (pg.45-57) ----
const AGENTIC_AI: SkillDef[] = [
  { slug: 'agentic.essentials', name: 'Agentic AI Essentials', domain: 'agentic-ai', description: 'Agent loop (perceive-plan-act-reflect), autonomy, memory, enterprise applications.' },
  { slug: 'agentic.architectures-design-patterns', name: 'Architectures & Design Patterns', domain: 'agentic-ai', description: 'ReAct, Plan-and-Execute, Reflexion; error handling, human-in-the-loop.' },
  { slug: 'agentic.langchain-lcel', name: 'Working with LangChain & LCEL', domain: 'agentic-ai', description: 'LCEL declarative pipelines, tool integration, persistent memory, async execution.' },
  { slug: 'agentic.langgraph-agents', name: 'Building AI Agents with LangGraph', domain: 'agentic-ai', description: 'Graph-based orchestration: nodes, edges, cycles, branching, fault tolerance.' },
  { slug: 'agentic.agentic-rag', name: 'Implementing Agentic RAG', domain: 'agentic-ai', description: 'Multi-step adaptive retrieval, hybrid retrieval, memory-augmented RAG.' },
  { slug: 'agentic.phidata-agents', name: 'Developing AI Agents with Phidata', domain: 'agentic-ai', description: 'Python-first agent framework: memory persistence, API/DB integration.' },
  { slug: 'agentic.multi-agent-systems', name: 'Multi-Agent Systems with LangGraph & CrewAI', domain: 'agentic-ai', description: 'Leader-worker vs peer-to-peer orchestration, role specialization, coordination.' },
  { slug: 'agentic.autogen-development', name: 'Advanced Agent Development with Autogen', domain: 'agentic-ai', description: 'Microsoft Autogen: modular agent roles, communication rules, multi-agent conversations.' },
  { slug: 'agentic.observability-agentops', name: 'AI Agent Observability & AgentOps', domain: 'agentic-ai', description: 'Tracing, LangSmith integration, lifecycle governance, runaway-agent prevention.' },
  { slug: 'agentic.rag-architecture-vector-dbs', name: 'RAG Architecture with Vector DBs', domain: 'agentic-ai', description: 'Vector DB fundamentals at scale, indexing algorithms, hybrid search schema design.' },
  { slug: 'agentic.advanced-rag-evaluation', name: 'Advanced RAG Patterns & Evaluation', domain: 'agentic-ai', description: 'Hierarchical RAG, context compression, automated benchmarks (RAGAS/HELM/GAIA).' },
  { slug: 'agentic.ci-cd-scaling', name: 'Continuous Integration, Deployment & Scaling for LLMs', domain: 'agentic-ai', description: 'CI/CD for agents, canary deployments, versioning, drift detection, secure pipelines.' },
];

const SKILLS: SkillDef[] = [
  ...PYTHON_STATS,
  ...APPLIED_ML,
  ...DEEP_LEARNING,
  ...NLP,
  ...GENAI_ESSENTIALS,
  ...LLM_APPS,
  ...AGENTIC_AI,
];

// Curriculum module order (1-7), used for domain_order -- NOT alphabetical.
const DOMAIN_ORDER: Record<string, number> = {
  'python-stats': 1,
  'applied-ml': 2,
  'deep-learning': 3,
  nlp: 4,
  'genai-essentials': 5,
  'llm-apps': 6,
  'agentic-ai': 7,
};

// REQUIRES edges: `from` must be learned before `to`, matching the
// curriculum's actual session/module order (linear across the whole
// course, same design choice as the DevOps track's graph -- confirmed
// with the user to keep, not split into independently-gated modules).
const EDGES: EdgeDef[] = [
  // Python & Stats internal sequence
  { from: 'python.fundamentals', to: 'python.data-structures-control-flow' },
  { from: 'python.data-structures-control-flow', to: 'python.functions-oop-exceptions' },
  { from: 'python.functions-oop-exceptions', to: 'python.data-analysis-visualization' },

  // Applied ML builds on Python/Stats
  { from: 'python.data-analysis-visualization', to: 'ml.data-preprocessing-feature-engineering' },
  { from: 'ml.data-preprocessing-feature-engineering', to: 'ml.supervised-regression' },
  { from: 'ml.supervised-regression', to: 'ml.supervised-classification' },
  { from: 'ml.supervised-classification', to: 'ml.unsupervised-learning' },
  { from: 'ml.unsupervised-learning', to: 'ml.clustering-dimensionality-reduction' },
  { from: 'ml.clustering-dimensionality-reduction', to: 'ml.ensemble-learning' },

  // Deep Learning builds on Applied ML
  { from: 'ml.ensemble-learning', to: 'dl.neural-network-foundations' },
  { from: 'dl.neural-network-foundations', to: 'dl.tuning-optimizing-networks' },
  { from: 'dl.tuning-optimizing-networks', to: 'dl.cnn-fundamentals' },
  { from: 'dl.cnn-fundamentals', to: 'dl.cnn-advanced' },
  { from: 'dl.cnn-advanced', to: 'dl.recurrent-networks' },
  { from: 'dl.recurrent-networks', to: 'dl.lstm-networks' },

  // NLP builds on Deep Learning
  { from: 'dl.lstm-networks', to: 'nlp.introduction' },
  { from: 'nlp.introduction', to: 'nlp.text-processing-feature-engineering' },
  { from: 'nlp.text-processing-feature-engineering', to: 'nlp.ner-parsing' },
  { from: 'nlp.ner-parsing', to: 'nlp.tokenization-encoding' },
  { from: 'nlp.tokenization-encoding', to: 'nlp.sentiment-analysis-essentials' },
  { from: 'nlp.sentiment-analysis-essentials', to: 'nlp.sentiment-analysis-advanced' },
  { from: 'nlp.sentiment-analysis-advanced', to: 'nlp.neural-language-models' },
  { from: 'nlp.neural-language-models', to: 'nlp.machine-translation' },
  { from: 'nlp.machine-translation', to: 'nlp.speech-multimodal' },

  // GenAI Essentials builds on NLP
  { from: 'nlp.speech-multimodal', to: 'genai.introduction' },
  { from: 'genai.introduction', to: 'genai.autoencoders-gans' },
  { from: 'genai.autoencoders-gans', to: 'genai.transformers-attention' },
  { from: 'genai.transformers-attention', to: 'genai.small-language-models' },
  { from: 'genai.small-language-models', to: 'genai.prompt-engineering-essentials' },
  { from: 'genai.prompt-engineering-essentials', to: 'genai.advanced-prompting' },

  // LLM Application Development builds on GenAI Essentials
  { from: 'genai.advanced-prompting', to: 'llm.introduction' },
  { from: 'llm.introduction', to: 'llm.open-source-model-variants' },
  { from: 'llm.open-source-model-variants', to: 'llm.vector-databases' },
  { from: 'llm.vector-databases', to: 'llm.rag-techniques' },
  { from: 'llm.rag-techniques', to: 'llm.frameworks-development' },
  { from: 'llm.frameworks-development', to: 'llm.application-development' },
  { from: 'llm.application-development', to: 'llm.fine-tuning-adaptation' },
  { from: 'llm.fine-tuning-adaptation', to: 'llm.application-deployment' },
  { from: 'llm.application-deployment', to: 'llm.caching-routing' },
  { from: 'llm.caching-routing', to: 'llm.evaluation-responsible-ai' },

  // Agentic AI & LLMOps builds on LLM Application Development
  { from: 'llm.evaluation-responsible-ai', to: 'agentic.essentials' },
  { from: 'agentic.essentials', to: 'agentic.architectures-design-patterns' },
  { from: 'agentic.architectures-design-patterns', to: 'agentic.langchain-lcel' },
  { from: 'agentic.langchain-lcel', to: 'agentic.langgraph-agents' },
  { from: 'agentic.langgraph-agents', to: 'agentic.agentic-rag' },
  { from: 'agentic.agentic-rag', to: 'agentic.phidata-agents' },
  { from: 'agentic.phidata-agents', to: 'agentic.multi-agent-systems' },
  { from: 'agentic.multi-agent-systems', to: 'agentic.autogen-development' },
  { from: 'agentic.autogen-development', to: 'agentic.observability-agentops' },
  { from: 'agentic.observability-agentops', to: 'agentic.rag-architecture-vector-dbs' },
  { from: 'agentic.rag-architecture-vector-dbs', to: 'agentic.advanced-rag-evaluation' },
  { from: 'agentic.advanced-rag-evaluation', to: 'agentic.ci-cd-scaling' },
];

async function main() {
  const db = new Kysely<Database>({
    dialect: new PostgresDialect({
      pool: new Pool({
        connectionString: process.env.DATABASE_URL ?? 'postgres://practice:practice@localhost:5433/practice_engine',
      }),
    }),
  });

  const skills = new SkillRepository(db);

  console.log(`Inserting ${SKILLS.length} skills across ${new Set(SKILLS.map((s) => s.domain)).size} domains for course=${COURSE_SLUG}...`);
  const idBySlug = new Map<string, string>();
  const domainSeq = new Map<string, number>();
  for (const s of SKILLS) {
    const sequence = (domainSeq.get(s.domain) ?? 0) + 1;
    domainSeq.set(s.domain, sequence);
    const row = await skills.createSkill({
      slug: s.slug,
      name: s.name,
      domain: s.domain,
      description: s.description,
      domainOrder: DOMAIN_ORDER[s.domain] ?? 999,
      sequence,
      courseSlug: COURSE_SLUG,
    });
    idBySlug.set(s.slug, row.id);
    console.log(`  + ${s.slug}`);
  }

  console.log(`Inserting ${EDGES.length} REQUIRES edges...`);
  for (const e of EDGES) {
    const fromId = idBySlug.get(e.from);
    const toId = idBySlug.get(e.to);
    if (!fromId || !toId) {
      console.warn(`  ! skipping edge ${e.from} -> ${e.to}: unknown slug`);
      continue;
    }
    await skills.addEdge({ fromSkillId: fromId, toSkillId: toId, type: 'REQUIRES' });
  }

  console.log('Rebuilding skill.skill_closure...');
  await skills.rebuildClosure();

  console.log(`Done: ${SKILLS.length} skills, ${EDGES.length} edges seeded for ${COURSE_SLUG}.`);
  await db.destroy();
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
