from django.test import TestCase
from django.urls import reverse
from rest_framework.test import APITestCase
from rest_framework import status
from apps.services.models import TTSService, LLMModel

from apps.inference.models import InferenceRequest

import pytest

class InferenceRequestTTSServiceFilterTests(APITestCase):
    def setUp(self):
        self.tts_service1 = TTSService.objects.create(slug='tts1', url='http://localhost:1234', type='Dia')
        self.tts_service2 = TTSService.objects.create(slug='tts2', url='http://localhost:5678', type='ChatTTS')
        self.req1 = InferenceRequest.objects.create(
            inference_type='tts',
            payload={'text_input': 'hello'},
            tts_service=self.tts_service1,
            status='completed'
        )
        self.req2 = InferenceRequest.objects.create(
            inference_type='tts',
            payload={'text_input': 'world'},
            tts_service=self.tts_service2,
            status='completed'
        )
        self.req3 = InferenceRequest.objects.create(
            inference_type='tts',
            payload={'text_input': 'foo'},
            tts_service=self.tts_service1,
            status='completed'
        )

    def test_filter_by_tts_service(self):
        url = reverse('inferencerequest-list')
        response = self.client.get(url, {'tts_service': self.tts_service1.id})
        self.assertEqual(response.status_code, status.HTTP_200_OK)
        ids = [r['id'] for r in response.data]
        self.assertIn(self.req1.id, ids)
        self.assertIn(self.req3.id, ids)
        self.assertNotIn(self.req2.id, ids)
        self.assertEqual(len(ids), 2)

    def test_filter_by_tts_service_no_results(self):
        # Use a non-existent service id
        url = reverse('inferencerequest-list')
        response = self.client.get(url, {'tts_service': 9999})
        self.assertEqual(response.status_code, status.HTTP_200_OK)
        self.assertEqual(len(response.data), 0)

class LLMInferenceTests(APITestCase):
    def setUp(self):
        self.llm_service = LLMModel.objects.create(
            name="Qwen/Qwen3-8B",
            base_url="http://192.168.5.173:8000/v1",
            is_active=True
        )

    # @pytest.mark.skip(reason="Requires local LLM service to be running")
    def test_llm_chat_completion(self):
        """
        Test LLM chat completion endpoint.

        To run this test in isolation:
        pytest backend/apps/inference/tests.py::LLMInferenceTests::test_llm_chat_completion -v -s
        """
        url = reverse('llm_inference')
        data = {
            "model": "Qwen/Qwen3-8B",
            "messages": [
                {"role": "user", "content": "What is 2+2?"}
            ],
            "temperature": 0.7
        }

        response = self.client.post(url, data, format='json')
        self.assertEqual(response.status_code, status.HTTP_200_OK)
        self.assertIn("choices", response.data)
        self.assertEqual(len(response.data["choices"]), 1)
        self.assertIn("message", response.data["choices"][0])
        self.assertIn("content", response.data["choices"][0]["message"])

        # Verify inference request was created
        inference_request = InferenceRequest.objects.first()
        self.assertIsNotNone(inference_request)
        self.assertEqual(inference_request.inference_type, "llm_chat")
        self.assertEqual(inference_request.status, "completed")
        self.assertEqual(inference_request.llm_service, self.llm_service)

    # @pytest.mark.skip(reason="Requires local LLM service to be running")
    def test_llm_text_completion(self):
        """
        Test LLM text completion endpoint.

        To run this test in isolation:
        pytest backend/apps/inference/tests.py::LLMInferenceTests::test_llm_text_completion -v -s
        """
        url = reverse('llm_inference')
        data = {
            "model": "Qwen/Qwen3-8B",
            "prompt": "What is 2+2?",
            "temperature": 0.7
        }

        response = self.client.post(url, data, format='json')
        self.assertEqual(response.status_code, status.HTTP_200_OK)
        self.assertIn("choices", response.data)
        self.assertEqual(len(response.data["choices"]), 1)
        self.assertIn("text", response.data["choices"][0])

        # Verify inference request was created
        inference_request = InferenceRequest.objects.first()
        self.assertIsNotNone(inference_request)
        self.assertEqual(inference_request.inference_type, "llm_completion")
        self.assertEqual(inference_request.status, "completed")
        self.assertEqual(inference_request.llm_service, self.llm_service)